package roard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/rexctl"
)

// uploads keeps large messages arriving in parts, under <store>/uploads/<id>.
// An upload is keyed by its message ID, so begin is idempotent, and it lives
// on disk, so it survives a roard restart. One timer covers the earliest
// expiry.
type uploads struct {
	d       Deps
	dir     string
	mu      sync.Mutex
	timer   *time.Timer
	stopped bool
}

type uploadMeta struct {
	Envelope string          `json:"envelope"`
	Files    []envelope.File `json:"files"`
	Began    time.Time       `json:"began"`
	Last     time.Time       `json:"last"`
}

const uploadPrefix = "up-"

var errBadUpload = errors.New("bad upload id")

func newUploads(d Deps) (*uploads, error) {
	u := &uploads{d: d, dir: filepath.Join(d.Store.Root(), "uploads")}
	if err := os.MkdirAll(u.dir, 0o700); err != nil {
		return nil, err
	}
	u.mu.Lock()
	u.arm()
	u.mu.Unlock()
	return u, nil
}

func (u *uploads) stop() {
	u.mu.Lock()
	u.stopped = true
	if u.timer != nil {
		u.timer.Stop()
	}
	u.mu.Unlock()
}

func (u *uploads) path(id string, parts ...string) string {
	return filepath.Join(append([]string{u.dir, id}, parts...)...)
}

func (u *uploads) load(id string) (uploadMeta, error) {
	var m uploadMeta
	b, err := os.ReadFile(u.path(id, "meta.json"))
	if err == nil {
		err = json.Unmarshal(b, &m)
	}
	return m, err
}

func (u *uploads) save(id string, m uploadMeta) error {
	b, _ := json.Marshal(m)
	tmp := u.path(id, "meta.json.tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, u.path(id, "meta.json"))
}

func (u *uploads) remove(id string) {
	u.mu.Lock()
	os.RemoveAll(u.path(id))
	u.arm()
	u.mu.Unlock()
}

// expired reports whether an upload is past idle_expiry or max_age.
func (u *uploads) expired(m uploadMeta, now time.Time) bool {
	s := u.d.Settings()
	return now.Sub(m.Last) >= s.Duration("roard.upload.idle_expiry") || now.Sub(m.Began) >= s.Duration("roard.upload.max_age")
}

func (u *uploads) deadline(m uploadMeta) time.Time {
	s := u.d.Settings()
	idle := m.Last.Add(s.Duration("roard.upload.idle_expiry"))
	if age := m.Began.Add(s.Duration("roard.upload.max_age")); age.Before(idle) {
		return age
	}
	return idle
}

// arm deletes expired uploads and sets one timer for the next. Called with mu.
func (u *uploads) arm() {
	if u.timer != nil {
		u.timer.Stop()
		u.timer = nil
	}
	if u.stopped {
		return
	}
	entries, _ := os.ReadDir(u.dir)
	now := time.Now()
	var next time.Time
	for _, e := range entries {
		m, err := u.load(e.Name())
		if err != nil || u.expired(m, now) {
			os.RemoveAll(u.path(e.Name()))
			continue
		}
		if dl := u.deadline(m); next.IsZero() || dl.Before(next) {
			next = dl
		}
	}
	if !next.IsZero() {
		u.timer = time.AfterFunc(time.Until(next), func() {
			u.mu.Lock()
			u.arm()
			u.mu.Unlock()
		})
	}
}

// begin starts an upload, or reports the parts already committed.
func (u *uploads) begin(raw, digest string) (any, *rexctl.Error) {
	sum := sha256.Sum256([]byte(raw))
	if hex.EncodeToString(sum[:]) != digest {
		return nil, refused("digest_mismatch")
	}
	var e envelope.Envelope
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return nil, refused("bad_envelope: %v", err)
	}
	if err := e.Validate(); err != nil {
		return nil, refused("bad_envelope: %v", err)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if m, err := u.load(e.ID); err == nil {
		if m.Envelope != raw {
			return nil, refused("upload_conflict: another envelope uses this ID")
		}
		return u.reply(e.ID, m), nil
	}
	entries, _ := os.ReadDir(u.dir)
	s := u.d.Settings()
	if len(entries) >= s.Int("roard.upload.max_count") {
		return nil, refused("upload_limit: %d uploads open", len(entries))
	}
	reserved := size(e.Files)
	for _, ent := range entries {
		if m, err := u.load(ent.Name()); err == nil {
			reserved += size(m.Files)
		}
	}
	if reserved > s.Size("roard.upload.max_bytes") {
		return nil, refused("upload_limit: %d bytes reserved", reserved)
	}
	now := time.Now()
	m := uploadMeta{Envelope: raw, Files: e.Files, Began: now, Last: now}
	if err := os.Mkdir(u.path(e.ID), 0o700); err != nil {
		return nil, &rexctl.Error{Code: "internal", Message: err.Error()}
	}
	for i := range e.Files {
		f, err := os.OpenFile(u.path(e.ID, partName(i)), os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, &rexctl.Error{Code: "internal", Message: err.Error()}
		}
		f.Close()
	}
	if err := u.save(e.ID, m); err != nil {
		return nil, &rexctl.Error{Code: "internal", Message: err.Error()}
	}
	u.arm()
	return u.reply(e.ID, m), nil
}

func size(files []envelope.File) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

// partName is the file holding attachment i; attachment names stay out of
// paths.
func partName(i int) string { return "f" + strconv.Itoa(i) }

func (u *uploads) reply(id string, m uploadMeta) map[string]any {
	committed := map[string]int64{}
	for i, f := range m.Files {
		if st, err := os.Stat(u.path(id, partName(i))); err == nil {
			committed[f.Name] = st.Size()
		}
	}
	return map[string]any{"upload": uploadPrefix + id, "committed": committed}
}

// contents reads a whole upload for commit.
func (u *uploads) contents(id string) (string, map[string][]byte, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	m, err := u.load(id)
	if err != nil {
		return "", nil, err
	}
	files := map[string][]byte{}
	for i, f := range m.Files {
		b, err := os.ReadFile(u.path(id, partName(i)))
		if err != nil {
			return "", nil, err
		}
		files[f.Name] = b
	}
	return m.Envelope, files, nil
}
