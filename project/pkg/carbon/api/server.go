// Package api is the Carbon Data Service HTTP surface (SCOPE-IN-02).
//
// Handlers read only the in-process cache. GET does not call the provider and
// does not advance replay model time (API-REST-02).
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"carbon-aware-k8s/api/v1alpha1"
	"carbon-aware-k8s/pkg/carbon/cache"
	"carbon-aware-k8s/pkg/metrics"
)

const (
	// readHeaderTimeout bounds header reads on the shared REST+/metrics listener (GO-09).
	readHeaderTimeout = 5 * time.Second
	// idleTimeout closes idle keep-alive connections.
	idleTimeout = 60 * time.Second
	// shutdownTimeout is the grace period for in-flight requests on SIGINT/SIGTERM.
	shutdownTimeout = 10 * time.Second
)

// Server is the CDS HTTP server. It does not store a context.Context (GO-08).
type Server struct {
	http     *http.Server
	cache    *cache.Cache
	provider v1alpha1.CarbonProvider
	metrics  *metrics.CDSMetrics
	logger   *slog.Logger
}

// New constructs a server bound to addr.
func New(addr string, c *cache.Cache, p v1alpha1.CarbonProvider, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		cache:    c,
		provider: p,
		metrics:  metrics.NewCDSMetrics(c),
		logger:   logger,
	}
	s.http = &http.Server{
		Addr:              addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
	return s
}

// Handler returns the HTTP handler (tests).
func (s *Server) Handler() http.Handler {
	return s.http.Handler
}

// Serve listens until ctx is cancelled, then shuts down gracefully.
func (s *Server) Serve(ctx context.Context) error {
	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("http listen", "addr", s.http.Addr)
		err := s.http.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		s.logger.Info("shutdown signal received")
		shCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := s.http.Shutdown(shCtx); err != nil {
			return fmt.Errorf("http shutdown: %w", err)
		}
		return <-errCh
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http listen: %w", err)
		}
		return nil
	}
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	// carbon_aware_api_requests_total is labelled by route pattern, not raw path.
	route := func(path string, h http.HandlerFunc) {
		mux.HandleFunc("GET "+path, s.metrics.Instrument(path, h))
	}
	route(v1alpha1.PathHealth, s.handleHealth)
	route(v1alpha1.PathHealthz, s.handleHealth)
	route(v1alpha1.PathZones, s.handleZones)
	route(v1alpha1.PathCurrent, s.handleCurrent)
	route(v1alpha1.PathForecast, s.handleForecast)
	route(v1alpha1.PathMetrics, s.handleMetrics)
	return mux
}
