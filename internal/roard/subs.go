package roard

import (
	"fmt"
	"sync"

	"github.com/vizkid/roar/internal/store"
)

// subs is every sender's interest in messages. A change is published once
// per revision; a sender that reconnects subscribes again and reads the
// current records, so a lost callback costs nothing but latency.
type subs struct {
	cb    *callbacks
	mu    sync.Mutex
	next  int
	byID  map[string]*sub
	byMsg map[string]map[*sub]bool
}

type sub struct {
	id, client string
	hints      Hints
	last       map[string]int64 // revision last sent, by message
	open       map[string]bool  // messages not yet in a final state
}

func newSubs(cb *callbacks) *subs {
	return &subs{cb: cb, byID: map[string]*sub{}, byMsg: map[string]map[*sub]bool{}}
}

// subscribe registers before it reads, so a change made while it reads is
// published too; the revision drops the copy.
func (s *subs) subscribe(client string, ids []string, h Hints, st *store.Store) (string, []Row) {
	s.mu.Lock()
	s.next++
	sb := &sub{id: fmt.Sprintf("sub-%d", s.next), client: client, hints: h, last: map[string]int64{}, open: map[string]bool{}}
	s.byID[sb.id] = sb
	for _, id := range ids {
		if s.byMsg[id] == nil {
			s.byMsg[id] = map[*sub]bool{}
		}
		s.byMsg[id][sb] = true
		sb.open[id] = true
	}
	s.mu.Unlock()
	rows := make([]Row, 0, len(ids))
	for _, id := range ids {
		m, err := st.Get(id)
		if err != nil {
			rows = append(rows, missing(id))
			continue
		}
		rows = append(rows, rowOf(m))
		s.mu.Lock()
		sb.last[id] = max(sb.last[id], m.Record.Revision)
		if m.Record.State.Final() {
			delete(sb.open, id)
		}
		s.mu.Unlock()
	}
	return sb.id, rows
}

func (s *subs) unsubscribe(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sb := s.byID[id]; sb != nil {
		s.remove(sb)
	}
}

// drop forgets a sender that left Rex.
func (s *subs) drop(client string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sb := range s.byID {
		if sb.client == client {
			s.remove(sb)
		}
	}
}

func (s *subs) remove(sb *sub) {
	delete(s.byID, sb.id)
	for id := range sb.last {
		delete(s.byMsg[id], sb)
		if len(s.byMsg[id]) == 0 {
			delete(s.byMsg, id)
		}
	}
	for id := range sb.open {
		delete(s.byMsg[id], sb)
		if len(s.byMsg[id]) == 0 {
			delete(s.byMsg, id)
		}
	}
}

// publish sends a change to every subscriber that has not seen this
// revision.
func (s *subs) publish(m store.Message) {
	id, rev := m.Envelope.ID, m.Record.Revision
	s.mu.Lock()
	var to []*sub
	for sb := range s.byMsg[id] {
		if sb.last[id] < rev {
			sb.last[id] = rev
			if m.Record.State.Final() {
				delete(sb.open, id)
			}
			to = append(to, sb)
		}
	}
	s.mu.Unlock()
	for _, sb := range to {
		if s.cb != nil {
			s.cb.send(sb.client, Result{Subscription: sb.id, Revision: rev, Rows: []Row{rowOf(m)}}, sb.hints)
		}
	}
}

// openIDs are subscribed messages not yet final. A writer from an older
// Roar leaves no notify mark, so the gap re-check reads these.
func (s *subs) openIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	var ids []string
	for _, sb := range s.byID {
		for id := range sb.open {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}
