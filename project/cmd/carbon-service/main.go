// Command carbon-service is the Carbon Data Service process (SCOPE-IN-02).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"carbon-aware-k8s/pkg/carbon/api"
	"carbon-aware-k8s/pkg/carbon/cache"
	"carbon-aware-k8s/pkg/carbon/config"
	"carbon-aware-k8s/pkg/carbon/provider"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], logger, os.Getenv); err != nil {
		logger.Error("carbon-service failed", "err", err)
		os.Exit(1)
	}
	logger.Info("carbon-service stopped")
}

func run(ctx context.Context, args []string, logger *slog.Logger, getenv config.LookupEnv) error {
	fs := flag.NewFlagSet("carbon-service", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "config.yaml", "path to YAML configuration file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cfg, err := config.Load(*configPath, getenv)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	logger.Info("configuration loaded", "path", *configPath, "config", cfg)

	prov, err := provider.NewProvider(&cfg)
	if err != nil {
		return fmt.Errorf("init provider: %w", err)
	}
	logger.Info("provider selected", "name", prov.Name(), "mode", prov.Mode())

	refreshTimeout := cfg.HTTPTimeout
	if refreshTimeout <= 0 {
		refreshTimeout = cache.DefaultRefreshTimeout
	}
	c := cache.NewWithOptions(cache.Options{
		Provider:        prov,
		Clock:           provider.ClockOf(prov),
		Logger:          logger,
		RefreshInterval: cfg.CacheTTL,
		RefreshTimeout:  refreshTimeout,
	})

	rctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	if err := c.Refresh(rctx); err != nil {
		logger.Warn("initial carbon cache refresh failed, serving empty until next tick", "err", err)
	}
	cancel()
	go func() {
		if err := c.Loop(ctx); err != nil {
			logger.Error("carbon cache loop stopped", "err", err)
		}
	}()

	srv := api.New(cfg.ListenAddr(), c, prov, logger)
	logger.Info("carbon-service starting", "addr", cfg.ListenAddr(), "cacheTTL", cfg.CacheTTL.String())

	if err := srv.Serve(ctx); err != nil {
		return err
	}
	return nil
}
