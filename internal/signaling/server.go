package signaling

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Session states implement the offer/answer state machine.
type sessionState string

const (
	stateNew     sessionState = "new"
	stateOpen    sessionState = "open"
	stateAnswered sessionState = "answered"
	stateClosed  sessionState = "closed"
)

// Counters aggregates protocol-level rejection statistics.
type Counters struct {
	DuplicateMessages int `json:"duplicate_messages"`
	StaleMessages     int `json:"stale_messages"`
	InvalidMessages   int `json:"invalid_messages"`
}

// Session is one offer/answer exchange between peer A and peer B.
type Session struct {
	mu       sync.Mutex
	ID       string
	state    sessionState
	nextSeq  map[string]uint64
	mailbox  map[string][]Message
	notify   map[string]chan struct{}
	counters Counters
	closedAt time.Time
}

func newSession(id string) *Session {
	return &Session{
		ID:      id,
		state:   stateNew,
		nextSeq: map[string]uint64{PeerA: 1, PeerB: 1},
		mailbox: map[string][]Message{PeerA: nil, PeerB: nil},
		notify:  map[string]chan struct{}{PeerA: make(chan struct{}, 1), PeerB: make(chan struct{}, 1)},
	}
}

// checkSeq enforces strictly monotonic per-peer sequence numbers.
func (s *Session) checkSeq(m *Message) error {
	want := s.nextSeq[m.From]
	switch {
	case m.Seq < want:
		if m.Seq == want-1 {
			s.counters.DuplicateMessages++
		} else {
			s.counters.StaleMessages++
		}
		return &StaleError{Peer: m.From, Got: m.Seq, Expected: want}
	case m.Seq > want:
		s.counters.StaleMessages++
		return &StaleError{Peer: m.From, Got: m.Seq, Expected: want}
	default:
		s.nextSeq[m.From] = want + 1
		return nil
	}
}

// apply runs payload validation, sequence checks and the state transition.
func (s *Session) apply(m *Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == stateClosed {
		return ErrSessionClosed
	}
	if err := m.ValidatePayload(); err != nil {
		s.counters.InvalidMessages++
		return err
	}
	if err := s.checkSeq(m); err != nil {
		return err
	}
	switch m.Type {
	case MsgOffer:
		if m.From != PeerA || s.state != stateNew {
			s.counters.InvalidMessages++
			return &ProtocolError{Reason: "offer only allowed from A in state new"}
		}
		s.state = stateOpen
	case MsgAnswer:
		if m.From != PeerB || s.state != stateOpen {
			s.counters.InvalidMessages++
			return &ProtocolError{Reason: "answer only allowed from B in state open"}
		}
		s.state = stateAnswered
	case MsgBye:
		s.state = stateClosed
		s.closedAt = time.Now()
	}
	s.mailbox[m.To] = append(s.mailbox[m.To], *m)
	select {
	case s.notify[m.To] <- struct{}{}:
	default:
	}
	return nil
}

// snapshot returns a copy of the pending mailbox for one peer.
func (s *Session) drain(peer string) []Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.mailbox[peer]
	s.mailbox[peer] = nil
	return out
}

// wait blocks until a message is queued, the context expires or the session
// closes. It returns true when a message may be available.
func (s *Session) wait(ctx context.Context, peer string) bool {
	s.mu.Lock()
	ch := s.notify[peer]
	closed := s.state == stateClosed
	has := len(s.mailbox[peer]) > 0
	s.mu.Unlock()
	if has || closed {
		return has
	}
	select {
	case <-ch:
		return true
	case <-ctx.Done():
		return false
	}
}

// Server is the loopback HTTP signaling service.
type Server struct {
	ln       net.Listener
	hs       *http.Server
	mu       sync.Mutex
	sessions map[string]*Session
}

// NewServer binds an ephemeral port on 127.0.0.1 only.
func NewServer() (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("signaling listen: %w", err)
	}
	s := &Server{ln: ln, sessions: map[string]*Session{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/session", s.handleCreate)
	mux.HandleFunc("/session/", s.handleSession)
	s.hs = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = s.hs.Serve(ln) }()
	return s, nil
}

// Addr reports the loopback base URL, e.g. http://127.0.0.1:53123.
func (s *Server) Addr() string { return "http://" + s.ln.Addr().String() }

// Shutdown releases the HTTP listener within the supplied deadline.
func (s *Server) Shutdown(ctx context.Context) error { return s.hs.Shutdown(ctx) }

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := fmt.Sprintf("s-%d", time.Now().UnixNano())
	s.mu.Lock()
	s.sessions[id] = newSession(id)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"session_id": id})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	parts := splitPath(r.URL.Path)
	if len(parts) < 2 {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	id, peer := parts[1], ""
	if len(parts) >= 3 {
		peer = parts[2]
	}
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	if sess == nil {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	switch {
	case r.Method == http.MethodPost:
		var m Message
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			sess.mu.Lock()
			sess.counters.InvalidMessages++
			sess.mu.Unlock()
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if err := sess.apply(&m); err != nil {
			code := http.StatusBadRequest
			if err == ErrSessionClosed {
				code = http.StatusGone
			}
			http.Error(w, err.Error(), code)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet:
		if peer != PeerA && peer != PeerB {
			http.Error(w, "unknown peer", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 250*time.Millisecond)
		defer cancel()
		sess.wait(ctx, peer)
		msgs := sess.drain(peer)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(msgs)
	case r.Method == http.MethodDelete:
		sess.mu.Lock()
		sess.state = stateClosed
		sess.closedAt = time.Now()
		sess.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// Counters returns the protocol counters for a session.
func (s *Server) Counters(id string) (Counters, bool) {
	s.mu.Lock()
	sess := s.sessions[id]
	s.mu.Unlock()
	if sess == nil {
		return Counters{}, false
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return sess.counters, true
}

func splitPath(p string) []string {
	var out []string
	cur := ""
	for _, c := range p {
		if c == '/' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
