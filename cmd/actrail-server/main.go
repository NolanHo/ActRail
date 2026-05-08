package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"actrail/internal/app"
	"actrail/internal/config"
	"actrail/internal/connectapi"
	"actrail/internal/httpapi"
	"actrail/internal/observability"
	"actrail/internal/ws"
	"go.uber.org/zap"
)

func main() {
	cfg := config.Load()
	if err := ensureDataDir(cfg); err != nil {
		_, _ = os.Stderr.WriteString("actrail data dir init failed: " + err.Error() + "\n")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger, shutdownObservability, err := observability.Setup(ctx, observability.FromEnv(cfg.Storage))
	if err != nil {
		_, _ = os.Stderr.WriteString("actrail observability init failed: " + err.Error() + "\n")
		os.Exit(1)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer cancel()
		if err := shutdownObservability(shutdownCtx); err != nil {
			_, _ = os.Stderr.WriteString("actrail observability shutdown failed: " + err.Error() + "\n")
		}
	}()

	service, err := app.NewStubWithDeferredRuntimeRestore(cfg)
	if err != nil {
		logger.Error("actrail service init failed", zap.String("sqlite_path", cfg.SQLitePath()), zap.Error(err))
		os.Exit(1)
	}
	defer func() {
		if err := service.Close(); err != nil {
			logger.Error("actrail service close failed", zap.Error(err))
		}
	}()
	replay, err := ws.NewReplayBuffer(cfg.Protocol.ResumeBuffer)
	if err != nil {
		logger.Error("invalid websocket replay buffer", zap.Error(err))
		os.Exit(1)
	}
	registry := ws.NewRegistry()
	connectBroker := connectapi.NewBroker(5000)
	publisher := ws.NewPublisher(registry, replay, ws.WithEventObserver(connectBroker))
	bridge := ws.NewAppBridge(service, service, publisher)
	service.SetRuntimeEventSink(bridge)
	handler := httpapi.New(cfg, service, ws.NewHandler(cfg,
		ws.WithRegistry(registry),
		ws.WithReplayBuffer(replay),
		ws.WithCommandTarget(bridge),
	), connectapi.NewHandler(service, connectBroker, connectapi.WithLogger(logger)))
	server := &http.Server{
		Addr:         cfg.Addr(),
		Handler:      observability.Handler("actrail.http", handler),
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		IdleTimeout:  cfg.Server.IdleTimeout,
	}

	go service.RunSupervisorScheduler(ctx)
	go service.RunSchedulerDeliverySweep(ctx)
	go service.RunWaitTimeoutSweep(ctx)
	go func() {
		if err := service.RestoreSurvivingRuntimes(ctx); err != nil {
			logger.Error("actrail runtime restore failed", zap.Error(err))
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("actrail server starting", zap.String("addr", cfg.Addr()), zap.Int("protocol_version", cfg.Protocol.Version), zap.String("data_dir", cfg.Storage.DataDir), zap.String("sqlite_path", cfg.SQLitePath()))
		errCh <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("actrail server shutdown failed", zap.Error(err))
			os.Exit(1)
		}
		logger.Info("actrail server stopped")
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("actrail server crashed", zap.Error(err))
			os.Exit(1)
		}
	}
}

func ensureDataDir(cfg config.Config) error {
	return cfg.Storage.EnsureDir()
}
