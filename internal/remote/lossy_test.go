package remote

import (
	"bytes"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vizkid/roar/internal/envelope"
	"github.com/vizkid/roar/internal/roard"
	"github.com/vizkid/roar/internal/store"
)

// lossy is a proxy in front of the Rex socket that delays every chunk in
// both directions and cuts each connection after cutAfter bytes, as a train
// link does. Raising the delay on loopback needs root; this needs nothing.
type lossy struct {
	sock     string
	delay    time.Duration
	cutAfter int64
	cuts     atomic.Int32
}

func newLossy(t *testing.T, upstream string, delay time.Duration, cutAfter int64) *lossy {
	dir, err := os.MkdirTemp("/tmp", "rl") // unix socket paths are short
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	l := &lossy{sock: filepath.Join(dir, "s"), delay: delay, cutAfter: cutAfter}
	ln, err := net.Listen("unix", l.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			u, err := net.Dial("unix", upstream)
			if err != nil {
				c.Close()
				continue
			}
			var moved atomic.Int64
			// Each chunk is delivered delay after it arrived: latency, not a
			// bandwidth cap.
			pipe := func(dst, src net.Conn) {
				type chunk struct {
					at   time.Time
					data []byte
				}
				q := make(chan chunk, 4096)
				go func() {
					defer close(q)
					for {
						buf := make([]byte, 32<<10)
						n, err := src.Read(buf)
						if n > 0 {
							q <- chunk{time.Now(), buf[:n]}
						}
						if err != nil {
							return
						}
					}
				}()
				defer dst.Close()
				defer src.Close()
				for c := range q {
					time.Sleep(time.Until(c.at.Add(l.delay)))
					if l.cutAfter > 0 && moved.Add(int64(len(c.data))) > l.cutAfter {
						l.cuts.Add(1)
						return // the link drops
					}
					if _, err := dst.Write(c.data); err != nil {
						return
					}
				}
			}
			go pipe(u, c)
			go pipe(c, u)
		}
	}()
	return l
}

// 65: over a link with a 2 s round trip that drops every connection after
// about 1 MiB, a 4 MiB send completes by resuming, and sends no committed
// byte twice.
func TestPeerSurvivesSlowLossyLink(t *testing.T) {
	for _, window := range []string{"1", "4"} {
		t.Run("in_flight="+window, func(t *testing.T) { lossySend(t, window) })
	}
}

func lossySend(t *testing.T, window string) {
	if os.Getenv("ROAR_SLOW") != "1" {
		t.Skip("set ROAR_SLOW=1: this runs for minutes")
	}
	f := startRoard(t, func(st *store.Store) roard.Delivery { return leaveQueued{} })
	upstream := strings.TrimPrefix(f.ep, "unix://")
	upstream = strings.ReplaceAll(upstream, "%20", " ")
	link := newLossy(t, upstream, time.Second, 1<<20)
	p := &Peer{Endpoint: "unix://" + link.sock, Label: f.label,
		Settings: settings(t, "sender.print_deadline=2s,sender.upload.part_size=256KiB,sender.connect.initial=200ms,sender.upload.in_flight="+window)}
	data := make([]byte, 4<<20)
	rand.Read(data)
	e := peerEnvelope("lossy")
	e.Files = []envelope.File{envelope.FileFor("f.bin", data)}
	start := time.Now()
	r, err := p.Send(t.Context(), e, map[string][]byte{"f.bin": data})
	t.Logf("4 MiB over a 2 s, lossy link: %v, %d cuts, %d calls, srtt %v", time.Since(start).Round(time.Second), link.cuts.Load(), p.Stats.Total(), p.SRTT().Round(time.Millisecond))
	if err != nil || r.State != "queued" {
		t.Fatalf("send %+v: %v", r, err)
	}
	if link.cuts.Load() == 0 {
		t.Fatal("the link never dropped; the test proves nothing")
	}
	// Only parts in flight when the link dropped are sent again: at most the
	// window, per drop. Committed bytes never are: each resume starts from
	// the offsets begin reports.
	inFlight, part := int64(p.Settings.Int("sender.upload.in_flight")), p.Settings.Size("sender.upload.part_size")*3/4
	if over := p.Stats.SentBytes - int64(len(data)); over < 0 || over > int64(link.cuts.Load())*inFlight*part {
		t.Fatalf("sent %d for %d over %d drops: resent more than was in flight", p.Stats.SentBytes, len(data), link.cuts.Load())
	}
	t.Logf("sent %d bytes for %d", p.Stats.SentBytes, len(data))
	m, err := f.st.Get(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(m.Dir, "files", "f.bin")); !bytes.Equal(got, data) {
		t.Fatal("stored file differs")
	}
}

// A small send over a link whose round trip is 2 s settles with default
// settings: deadlines stretch with the measured round trip.
func TestPeerSmallSendOverSlowLink(t *testing.T) {
	f := startRoard(t, func(st *store.Store) roard.Delivery { return markRead{st} })
	upstream := strings.ReplaceAll(strings.TrimPrefix(f.ep, "unix://"), "%20", " ")
	link := newLossy(t, upstream, time.Second, 0)
	p := &Peer{Endpoint: "unix://" + link.sock, Label: f.label, Settings: settings(t, "")}
	r, err := p.Send(t.Context(), peerEnvelope("slow hi"), nil)
	if err != nil || r.State != "read" {
		t.Fatalf("send %+v: %v", r, err)
	}
}
