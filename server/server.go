package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	"github.com/kdwils/magnetite/service"
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
}

func New(port int, logger *slog.Logger, svc Service) Server {
	return Server{
		port:   port,
		logger: logger,
		svc:    svc,
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

	return s.gracefullyListenAndServe(ctx, srv, "HTTP server")
}

func (s *Server) gracefullyListenAndServe(ctx context.Context, srv *http.Server, name string) error {
	go func(ctx context.Context) {
		<-ctx.Done()
		s.logger.Info("shutting down", "name", name)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			s.logger.Error("error shutting down", "name", name, "error", err)
			return
		}
		s.logger.Info("shutdown complete", "name", name)
	}(ctx)

	s.logger.Info("server listening", "name", name, "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("%s failed: %w", name, err)
	}

	return nil
}
