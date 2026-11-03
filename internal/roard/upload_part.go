package roard

import (
	"bytes"
	"encoding/base64"
	"io"
	"os"
	"strings"
	"time"

	"github.com/vizkid/rexkit/rexctl"
	"github.com/vizkid/roar/internal/envelope"
)

func uploadID(up string) (string, bool) {
	id, ok := strings.CutPrefix(up, uploadPrefix)
	return id, ok && envelope.ValidID(id)
}

// part appends one part. A part that repeats committed bytes is accepted;
// one that differs, skips ahead, or passes the declared size is refused.
func (u *uploads) part(up, name string, off int64, data string) (any, *rexctl.Error) {
	id, ok := uploadID(up)
	if !ok {
		return nil, badRequest(errBadUpload)
	}
	b, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, badRequest(err)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	m, err := u.load(id)
	if err != nil {
		return nil, refused("upload_unknown: no upload %s; it may have expired", up)
	}
	now := time.Now()
	if u.expired(m, now) {
		u.drop(id)
		u.arm()
		return nil, refused("upload_expired")
	}
	i := -1
	for j, f := range m.Files {
		if f.Name == name {
			i = j
		}
	}
	if i < 0 {
		return nil, refused("upload_unknown_file: %s", name)
	}
	path := u.path(id, partName(i))
	st, err := os.Stat(path)
	if err != nil {
		return nil, &rexctl.Error{Code: "internal", Message: err.Error()}
	}
	committed := st.Size()
	switch {
	case off < 0 || off+int64(len(b)) > m.Files[i].Size:
		return nil, refused("upload_too_large: %s is %d bytes", name, m.Files[i].Size)
	case off > committed:
		return nil, refused("upload_gap: committed %d", committed)
	}
	if overlap := min(int64(len(b)), committed-off); overlap > 0 {
		have := make([]byte, overlap)
		f, err := os.Open(path)
		if err == nil {
			_, err = f.ReadAt(have, off)
			f.Close()
		}
		if err != nil && err != io.EOF || !bytes.Equal(have, b[:overlap]) {
			return nil, refused("upload_conflict: bytes at %d differ", off)
		}
		b = b[overlap:]
	}
	if len(b) > 0 {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
		if err == nil {
			_, err = f.Write(b)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			return nil, &rexctl.Error{Code: "internal", Message: err.Error()}
		}
		committed += int64(len(b))
	}
	m.Last = now
	// The bytes may have landed already. A retry of this part is accepted as
	// a repeat, so failing here only costs that retry.
	if err := u.save(id, m); err != nil {
		return nil, &rexctl.Error{Code: "internal", Message: "upload metadata: " + err.Error()}
	}
	u.arm()
	return map[string]any{"committed": committed}, nil
}
