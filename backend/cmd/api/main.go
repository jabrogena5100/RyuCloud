// Command api runs the RyuCloud HTTP API.
//
// Usage:
//
//	api                          start the server (same as "api serve")
//	api migrate [up|down|reset|status]
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
	"github.com/jabrogena5100/RyuCloud/backend/internal/database"
	"github.com/jabrogena5100/RyuCloud/backend/internal/integrations"
	"github.com/jabrogena5100/RyuCloud/backend/internal/server"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	logger := newLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	command := "serve"
	if len(args) > 0 {
		command = args[0]
	}
	switch command {
	case "serve":
		return serve(ctx, cfg, logger)
	case "migrate":
		sub := "up"
		if len(args) > 1 {
			sub = args[1]
		}
		return database.Migrate(ctx, cfg.DatabaseURL, sub)
	default:
		return fmt.Errorf("unknown command %q (use: serve | migrate [up|down|reset|status])", command)
	}
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	storage, err := integrations.NewStorage(ctx, cfg)
	if err != nil {
		return err
	}
	logger.Info("object storage", "backend", cfg.StorageBackend, "bucket", cfg.S3.Bucket)

	queue, err := integrations.NewQueue(ctx, cfg)
	if err != nil {
		return err
	}
	if c, ok := queue.(io.Closer); ok {
		defer c.Close()
	}
	logger.Info("job queue", "backend", cfg.QueueBackend)

	playback, err := integrations.NewPlayback(cfg, server.APIPrefix)
	if err != nil {
		return err
	}
	if cf, ok := playback.(*integrations.CloudFrontPlayback); ok {
		cf.OnError = func(videoID string, err error) {
			logger.Error("could not sign CloudFront access", "video_id", videoID, "error", err)
		}
	}
	logger.Info("media delivery", "backend", cfg.PlaybackBackend)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           server.New(cfg, pool, logger, server.WithStorage(storage), server.WithQueue(queue), server.WithPlayback(playback)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", srv.Addr, "environment", cfg.Environment)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l}))
}
