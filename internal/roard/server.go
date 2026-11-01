package roard

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/rexctl"
	"github.com/vizkid/roar/internal/store"
)

// actions are what roard offers. Names have a dot: Rex keeps names without
// one for its configuration file.
var actions = []rexctl.Action{
	{Name: "roar.hello", Title: "Roar: hello", Description: "Version, host and part size"},
	{Name: "roar.subscribe", Title: "Roar: subscribe", Description: "Current records, then each change by roar.result"},
	{Name: "roar.unsubscribe", Title: "Roar: unsubscribe"},
	{Name: "roar.enqueue", Title: "Roar: enqueue", Description: "Store a message from another host"},
	{Name: "roar.upload.begin", Title: "Roar: begin upload", Description: "Start a message too large for one call, or resume it"},
	{Name: "roar.upload.part", Title: "Roar: upload part"},
	{Name: "roar.upload.commit", Title: "Roar: commit upload", Description: "Store an uploaded message"},
	{Name: "roar.status", Title: "Roar: status", Description: "Records of messages held here, by roar.result"},
	{Name: "roar.panes", Title: "Roar: panes", Description: "This host's panes, by roar.result"},
}

// server answers roard's actions. Each handler checks and records, then
// returns; real work runs in the pool and reports by callback.
type server struct {
	d    Deps
	subs *subs
	cb   *callbacks
	pool *pool
	w    *watcher
	up   *uploads
	jobs atomic.Int64
	ctx  context.Context
}

func refused(format string, args ...any) *rexctl.Error {
	return &rexctl.Error{Code: "refused", Message: fmt.Sprintf(format, args...)}
}

func badRequest(err error) *rexctl.Error {
	return &rexctl.Error{Code: "bad_request", Message: err.Error()}
}

func (s *server) perform(p rexctl.Perform) (any, *rexctl.Error) {
	start := time.Now()
	res, err := s.dispatch(p)
	if took := time.Since(start); took > s.d.Settings().Duration("roard.handler.warn") {
		s.d.logf("slow handler %s: %v", p.Action, took)
	}
	return res, err
}

func (s *server) dispatch(p rexctl.Perform) (any, *rexctl.Error) {
	if !allowed(p.Requester.Principal, s.d.UID) {
		s.d.logf("refused %s from %s (%s)", p.Action, p.Requester.Principal.Display, p.Requester.Principal.Kind)
		return nil, refused("caller_not_allowed: %s", p.Requester.Principal.Kind)
	}
	client := p.Requester.ClientID
	switch p.Action {
	case "roar.hello":
		return map[string]any{"version": s.d.Version, "host_id": s.d.HostID, "protocol": Protocol, "max_part": s.maxPart()}, nil
	case "roar.subscribe":
		var a struct {
			IDs   []string `json:"ids"`
			Hints Hints    `json:"hints"`
		}
		if err := json.Unmarshal(p.Args, &a); err != nil {
			return nil, badRequest(err)
		}
		for _, id := range a.IDs {
			if !envelope.ValidID(id) {
				return nil, badRequest(fmt.Errorf("bad message id %q", id))
			}
		}
		id, rows := s.subs.subscribe(client, a.IDs, a.Hints, s.d.Store)
		s.w.mu.Lock()
		s.w.armGap()
		s.w.mu.Unlock()
		return map[string]any{"subscription": id, "rows": rows}, nil
	case "roar.unsubscribe":
		var a struct {
			Subscription string `json:"subscription"`
		}
		json.Unmarshal(p.Args, &a)
		s.subs.unsubscribe(a.Subscription)
		return map[string]any{"ok": true}, nil
	case "roar.enqueue":
		return s.enqueue(client, p.Args)
	case "roar.upload.begin":
		var a struct {
			Envelope string `json:"envelope"`
			SHA256   string `json:"sha256"`
		}
		if err := json.Unmarshal(p.Args, &a); err != nil {
			return nil, badRequest(err)
		}
		return s.up.begin(a.Envelope, a.SHA256)
	case "roar.upload.part":
		var a struct {
			Upload string `json:"upload"`
			File   string `json:"file"`
			Offset int64  `json:"offset"`
			Data   string `json:"data"`
		}
		if err := json.Unmarshal(p.Args, &a); err != nil {
			return nil, badRequest(err)
		}
		return s.up.part(a.Upload, a.File, a.Offset, a.Data)
	case "roar.upload.commit":
		var a struct {
			Upload string `json:"upload"`
		}
		if err := json.Unmarshal(p.Args, &a); err != nil {
			return nil, badRequest(err)
		}
		id, ok := uploadID(a.Upload)
		if !ok {
			return nil, badRequest(errBadUpload)
		}
		return s.job(client, func(job string) Result { return s.commit(job, id) })
	case "roar.status":
		var a struct {
			IDs []string `json:"ids"`
		}
		if err := json.Unmarshal(p.Args, &a); err != nil {
			return nil, badRequest(err)
		}
		return s.job(client, func(job string) Result { return Result{Job: job, Rows: s.status(a.IDs)} })
	case "roar.panes":
		if s.d.Panes == nil {
			return nil, &rexctl.Error{Code: "not_supported", Message: "this roard lists no panes"}
		}
		return s.job(client, func(job string) Result {
			panes, err := s.d.Panes(s.ctx)
			if err != nil {
				return Result{Job: job, Error: &rexctl.Error{Code: "internal", Message: err.Error()}}
			}
			return Result{Job: job, Panes: panes}
		})
	}
	return nil, &rexctl.Error{Code: "not_supported", Message: "no action " + p.Action}
}

// maxPart is the base64 bytes a part may carry: room under the server's
// message limit for the JSON around it and Rex's forwarded event.
func (s *server) maxPart() int {
	if c := s.cb.sess.Load(); c != nil {
		if cl := c.Current(); cl != nil {
			return cl.MaxMessage() * 3 / 4
		}
	}
	return 48 << 10
}

// job runs work in the pool and calls the result back.
func (s *server) job(client string, work func(job string) Result) (any, *rexctl.Error) {
	job := fmt.Sprintf("job-%d", s.jobs.Add(1))
	if err := s.pool.submit(func() { s.cb.send(client, work(job), Hints{}) }); err != nil {
		return nil, refused("%v", err)
	}
	return map[string]any{"job": job}, nil
}

func (s *server) status(ids []string) []Row {
	rows := make([]Row, 0, len(ids))
	for _, id := range ids {
		m, err := s.d.Store.Get(id)
		if err != nil {
			rows = append(rows, missing(id))
		} else {
			rows = append(rows, rowOf(m))
		}
	}
	return rows
}

// enqueue checks the digest in the handler, which is fast at the inline
// size, and stores in the pool.
func (s *server) enqueue(client string, args json.RawMessage) (any, *rexctl.Error) {
	var a struct {
		Envelope string            `json:"envelope"`
		SHA256   string            `json:"sha256"`
		Files    map[string]string `json:"files"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, badRequest(err)
	}
	sum := sha256.Sum256([]byte(a.Envelope))
	if hex.EncodeToString(sum[:]) != a.SHA256 {
		return nil, refused("digest_mismatch")
	}
	return s.job(client, func(job string) Result {
		files := map[string][]byte{}
		for name, b64 := range a.Files {
			b, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return Result{Job: job, Error: refused("bad_attachment: %s", name)}
			}
			files[name] = b
		}
		return s.store(job, []byte(a.Envelope), files)
	})
}

// store saves a message from another host. A duplicate returns the stored
// record and is attempted again, so a retry also repairs a missing watcher.
func (s *server) store(job string, raw []byte, files map[string][]byte) Result {
	var e envelope.Envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		return Result{Job: job, Error: refused("bad_envelope: %v", err)}
	}
	e.Forwarded = true
	if e.To.HostID != s.d.HostID {
		return Result{Job: job, Error: refused("wrong_host: the message is for host %s, this is %s", e.To.HostID, s.d.HostID)}
	}
	now := time.Now()
	s.d.Store.Prune(now)
	dup, err := s.d.Store.PutWithFiles(e, now, files)
	if err != nil {
		if errors.Is(err, store.ErrLimit) {
			return Result{Job: job, Error: refused("%v", err)}
		}
		return Result{Job: job, Error: refused("store: %v", err)}
	}
	m, err := s.d.Store.Get(e.ID)
	if err != nil {
		return Result{Job: job, Error: &rexctl.Error{Code: "internal", Message: err.Error()}}
	}
	if dup {
		s.w.cover(e.To.Block)
	}
	return Result{Job: job, Rows: []Row{rowOf(m)}}
}

// commit stores an uploaded message. A commit retried after the message was
// stored returns the stored record; a damaged upload is deleted, so the
// sender's next begin starts over.
func (s *server) commit(job, id string) Result {
	if m, err := s.d.Store.Get(id); err == nil {
		s.up.remove(id)
		s.w.cover(m.Envelope.To.Block)
		return Result{Job: job, Rows: []Row{rowOf(m)}}
	}
	raw, files, err := s.up.contents(id)
	if err != nil {
		return Result{Job: job, Error: refused("upload_unknown: %s; it may have expired", id)}
	}
	r := s.store(job, []byte(raw), files)
	if r.Error == nil || r.Error.Code == "refused" {
		s.up.remove(id)
	}
	return r
}
