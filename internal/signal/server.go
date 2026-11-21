package signal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	PathOpen   = "/signal/open"
	PathOffer  = "/signal/offer/"
	PathAnswer = "/signal/answer/"
	PathState  = "/signal/state"
	PathFinish = "/signal/finish"
	maxBody    = 1 << 20
)

// Server exposes the Store over loopback HTTP.
type Server struct {
	store  *Store
	mux    *http.ServeMux
	http   *http.Server
	ln     net.Listener
	mu     sync.Mutex
	closed bool
}

// NewServer binds a loopback listener and prepares the HTTP mux.
func NewServer(listen string, timeout time.Duration) (*Server, error) {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", listen, err)
	}
	if err := requireLoopback(ln.Addr()); err != nil {
		_ = ln.Close()
		return nil, err
	}
	s := &Server{store: NewStore(timeout), ln: ln}
	s.mux = http.NewServeMux()
	for _, install := range routeInstallers {
		install(s.mux, s)
	}
	s.http = &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	return s, nil
}

// Addr returns the bound loopback address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// BaseURL returns the loopback base URL for clients, preserving the bound
// loopback family (IPv4 or IPv6) so both are reachable.
func (s *Server) BaseURL() string {
	addr := s.ln.Addr().(*net.TCPAddr) //nolint:forcetypeassert // bound above
	return fmt.Sprintf("http://%s", net.JoinHostPort(addr.IP.String(), strconv.Itoa(addr.Port)))
}

// Store exposes the underlying state machine for waiters.
func (s *Server) Store() *Store { return s.store }

// Serve blocks until Shutdown is called.
func (s *Server) Serve() error {
	err := s.http.Serve(s.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the listener and terminates any open round.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()

	if snap := s.store.Snapshot(); snap != nil && !snap.Closed {
		_ = s.store.Abort(snap.RequestID)
	}
	return s.http.Shutdown(ctx)
}
