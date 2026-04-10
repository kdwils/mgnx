package torznab

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/kdwils/mgnx/recorder"
	"github.com/kdwils/mgnx/service"
)

type Service interface {
	Caps() service.XMLCaps
	Search(ctx context.Context, req service.SearchRequest) (service.XMLRSS, error)
	SearchMovies(ctx context.Context, req service.MovieSearchRequest) (service.XMLRSS, error)
	SearchTV(ctx context.Context, req service.TVSearchRequest) (service.XMLRSS, error)
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
	r := mux.NewRouter()
	r.Use(WithLogger(s.logger))
	r.HandleFunc("/api", s.handleAPI()).Methods(http.MethodGet)

	corsHandler := handlers.CORS(
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowedMethods([]string{"GET", "POST", "OPTIONS"}),
		handlers.AllowedHeaders([]string{"Content-Type"}),
	)(r)

	srv := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: corsHandler,
	}

	return s.gracefullyListenAndServe(ctx, srv, nil)
}

// ServeListener starts the server on an already-bound listener.
// Tests use net.Listen("tcp", ":0") to get an ephemeral port without a port-reuse race.
func (s *Server) ServeListener(ctx context.Context, l net.Listener) error {
	r := mux.NewRouter()
	r.Use(WithLogger(s.logger))
	r.HandleFunc("/api", s.handleAPI()).Methods(http.MethodGet)

	corsHandler := handlers.CORS(
		handlers.AllowedOrigins([]string{"*"}),
		handlers.AllowedMethods([]string{"GET", "POST", "OPTIONS"}),
		handlers.AllowedHeaders([]string{"Content-Type"}),
	)(r)

	srv := &http.Server{Handler: corsHandler}
	return s.gracefullyListenAndServe(ctx, srv, l)
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

	s.logger.Info("torznab server listening", "addr", srv.Addr)

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
