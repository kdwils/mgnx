package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/kdwils/mgnx/db/gen"
	"github.com/kdwils/mgnx/recorder"
	"github.com/kdwils/mgnx/service"
)

// Service is the interface the API server depends on for all handlers.
type Service interface {
	Caps() service.XMLCaps
	Search(ctx context.Context, req service.SearchRequest) (service.XMLRSS, error)
	SearchMovies(ctx context.Context, req service.MovieSearchRequest) (service.XMLRSS, error)
	SearchTV(ctx context.Context, req service.TVSearchRequest) (service.XMLRSS, error)
	ListTorrents(ctx context.Context, req service.ListTorrentsRequest) ([]gen.ListTorrentsRow, error)
	GetTorrent(ctx context.Context, infohash string) (gen.GetTorrentByInfohashRow, error)
	UpdateTorrentState(ctx context.Context, req service.UpdateTorrentStateRequest) error
	DeleteTorrent(ctx context.Context, infohash string) error
}

type Server struct {
	port   int
	logger *slog.Logger
	svc    Service
	rec    *recorder.Recorder
}

func New(port int, logger *slog.Logger, svc Service, rec *recorder.Recorder) Server {
	return Server{
		port:   port,
		logger: logger,
		svc:    svc,
		rec:    rec,
	}
}

func (s *Server) Serve(ctx context.Context) error {
	r := s.buildRouter()
	corsHandler := corsMiddleware(r)
	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: corsHandler,
	}
	return s.gracefullyListenAndServe(ctx, srv, nil)
}

// ServeListener starts the server on an already-bound listener
func (s *Server) ServeListener(ctx context.Context, l net.Listener) error {
	r := s.buildRouter()
	corsHandler := corsMiddleware(r)
	srv := &http.Server{Handler: corsHandler}
	return s.gracefullyListenAndServe(ctx, srv, l)
}

func (s *Server) buildRouter() *mux.Router {
	r := mux.NewRouter()
	r.Use(withLogger(s.logger))

	r.HandleFunc("/api/torznab", s.handleTorznab()).Methods(http.MethodGet)

	r.HandleFunc("/api/torrents", s.handleListTorrents()).Methods(http.MethodGet)
	r.HandleFunc("/api/torrents/{infohash}", s.handleGetTorrent()).Methods(http.MethodGet)
	r.HandleFunc("/api/torrents/{infohash}", s.handleUpdateTorrent()).Methods(http.MethodPatch)
	r.HandleFunc("/api/torrents/{infohash}", s.handleDeleteTorrent()).Methods(http.MethodDelete)

	return r
}

func (s *Server) gracefullyListenAndServe(ctx context.Context, srv *http.Server, l net.Listener) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			s.logger.Error("error shutting down HTTP server", "error", err)
		}
	}()

	s.logger.Info("api server listening", "addr", srv.Addr)

	if l != nil {
		if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("HTTP server failed: %w", err)
		}
		return nil
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("HTTP server failed: %w", err)
	}
	return nil
}

func corsMiddleware(h http.Handler) http.Handler {
	return handlers.CORS(
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowedMethods([]string{"GET", "POST", "PATCH", "DELETE", "OPTIONS"}),
		handlers.AllowedHeaders([]string{"Content-Type"}),
	)(h)
}
