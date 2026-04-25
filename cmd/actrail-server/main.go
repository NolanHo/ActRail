package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"actrail/internal/app"
	"actrail/internal/config"
	"actrail/internal/httpapi"
	"actrail/internal/ws"
)

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	service := app.NewStub(cfg)
	handler := httpapi.New(cfg, service, ws.NewHandler(cfg))
	server := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("actrail server starting", "addr", cfg.Addr(), "protocol_version", cfg.Protocol.Version)
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("actrail server shutdown failed", "err", err)
			os.Exit(1)
		}
		logger.Info("actrail server stopped")
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("actrail server crashed", "err", err)
			os.Exit(1)
		}
	}
}
