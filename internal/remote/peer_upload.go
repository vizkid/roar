package remote

import (
	"context"
	"encoding/base64"
)

// handOver enqueues inline when everything fits one part, else uploads.
func (p *Peer) handOver(ctx context.Context, l *link, st *sendState) error {
	args := map[string]any{"envelope": string(st.raw), "sha256": digest(st.raw)}
	if int64(len(st.raw))+encodedSize(st.files)+4096 <= int64(l.maxPart) {
		inline := map[string]string{}
		for name, b := range st.files {
			inline[name] = base64.StdEncoding.EncodeToString(b)
		}
		args["files"] = inline
		st.committed = true
		return p.call(ctx, l, "roar.enqueue", args, nil)
	}
	if err := p.upload(ctx, l, st, args); err != nil {
		return err
	}
	st.committed = true
	return p.call(ctx, l, "roar.upload.commit", map[string]any{"upload": "up-" + st.e.ID}, nil)
}

// upload sends each file from its committed offset, keeping in_flight
// parts outstanding. They go out in order on one connection, and Rex hands
// them to roard in that order, so each lands at the offset it names.
func (p *Peer) upload(ctx context.Context, l *link, st *sendState, begin map[string]any) error {
	var b struct {
		Upload    string           `json:"upload"`
		Committed map[string]int64 `json:"committed"`
	}
	if err := p.call(ctx, l, "roar.upload.begin", begin, &b); err != nil {
		return err
	}
	s := p.Settings
	// Parts can land while their replies are lost to a drop. begin shows
	// them, and that progress extends the deadline like an acknowledgement.
	var landed int64
	for _, c := range b.Committed {
		landed += c
	}
	if landed > st.landed {
		st.landed = landed
		st.extend(s.Duration("sender.transfer.base"))
	}
	// part_size is base64 bytes; capped so the request stays under the limit.
	// A receiver's max_part below one base64 group would give 0: no part
	// could carry a byte, and the loop would never move.
	raw := max(1, int64(min(s.Size("sender.upload.part_size"), int64(l.maxPart)))*3/4)
	window := max(1, s.Int("sender.upload.in_flight"))
	type sent struct {
		off  int64
		wait func(context.Context, any) error
	}
	n := 0
	for _, f := range st.e.Files {
		data := st.files[f.Name]
		next := b.Committed[f.Name]
		var pending []sent
		for next < int64(len(data)) || len(pending) > 0 {
			for len(pending) < window && next < int64(len(data)) {
				end := min(next+raw, int64(len(data)))
				wait, err := p.start(l, "roar.upload.part", map[string]any{"upload": b.Upload, "file": f.Name, "offset": next,
					"data": base64.StdEncoding.EncodeToString(data[next:end])})
				if err != nil {
					return err
				}
				pending = append(pending, sent{next, wait})
				p.mu.Lock()
				p.Stats.SentBytes += end - next
				p.mu.Unlock()
				next = end
			}
			head := pending[0]
			pending = pending[1:]
			var r struct {
				Committed int64 `json:"committed"`
			}
			if err := p.finish(ctx, head.wait, &r); err != nil {
				for _, rest := range pending {
					p.finish(ctx, rest.wait, nil) // drain; begin reports what landed
				}
				return err
			}
			p.mu.Lock()
			p.Stats.PartBytes += max(0, r.Committed-head.off)
			p.mu.Unlock()
			// A moving upload never runs out of time; a part that adds nothing
			// does not move it.
			if r.Committed > head.off {
				st.extend(s.Duration("sender.transfer.base"))
			}
			n++
			if p.afterPart != nil {
				p.afterPart(l.c, n)
			}
		}
	}
	return nil
}
