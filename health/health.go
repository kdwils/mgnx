package health

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NodeCounter is the subset of dht.Crawler needed for the readiness check.
// Using a narrow interface keeps health decoupled from the full Crawler interface.
type NodeCounter interface {
	NodeCount() int
}

// State holds the readiness gate values.
type State struct {
	pool           *pgxpool.Pool
	crawler        NodeCounter
	crawlerEnabled bool
}

// Server owns the HTTP listener and health state.
type Server struct {
	port  int
	srv   *http.Server
	state *State
}

// New constructs a Server.
func New(port int, pool *pgxpool.Pool, crawler NodeCounter, crawlerEnabled bool) *Server {
	s := &Server{
		port: port,
		state: &State{
			pool:           pool,
			crawler:        crawler,
			crawlerEnabled: crawlerEnabled,
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/liveness", s.handleLive)
	mux.HandleFunc("/readiness", s.handleReady)

	s.srv = &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: mux,
	}
	return s
}

// Start binds the listener and serves until ctx is cancelled, then shuts down
// gracefully. Returns a bind error immediately if the port is unavailable.
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return fmt.Errorf("health server: bind :%d: %w", s.port, err)
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutCtx)
	}()

	if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("health server: %w", err)
	}
	return nil
}

func (s *Server) handleLive(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

type HealthCheck struct {
	Ok      bool     `json:"ok"`
	Reasons []string `json:"reasons,omitempty"`
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ok, reasons := s.state.check(r.Context())
	resp := HealthCheck{Ok: ok, Reasons: reasons}

	w.Header().Set("Content-Type", "application/json")
	status := http.StatusOK
	if !ok {
		status = http.StatusServiceUnavailable
	}

	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

// check evaluates all readiness gates in order. Returns false + reason on first failure.
func (s *State) check(ctx context.Context) (bool, []string) {
	reasons := make([]string, 0)
	if s.pool == nil {
		reasons = append(reasons, "db not initialized")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := s.pool.Ping(pingCtx); err != nil {
		reasons = append(reasons, "db ping failed")
	}

	if s.crawlerEnabled && s.crawler.NodeCount() == 0 {
		reasons = append(reasons, "routing table node count is 0")
	}

	return len(reasons) == 0, reasons
}
