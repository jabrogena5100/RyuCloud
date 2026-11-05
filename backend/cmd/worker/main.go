// Command worker processes uploaded videos: it takes jobs from the queue, runs
// ffprobe/ffmpeg, stores HLS renditions and a thumbnail in object storage, and marks
// the video ready. It runs independently of the HTTP API; they share only PostgreSQL,
// the queue and object storage.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jabrogena5100/RyuCloud/backend/internal/config"
	"github.com/jabrogena5100/RyuCloud/backend/internal/database"
	"github.com/jabrogena5100/RyuCloud/backend/internal/integrations"
	"github.com/jabrogena5100/RyuCloud/backend/internal/jobs"
	"github.com/jabrogena5100/RyuCloud/backend/internal/transcode"
	"github.com/jabrogena5100/RyuCloud/backend/internal/worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	if cfg.QueueBackend == "disabled" || cfg.QueueBackend == "memory" {
		return errors.New("the worker needs a shared queue: set QUEUE_BACKEND=redis (and REDIS_URL)")
	}
	if cfg.StorageBackend != "s3" {
		return errors.New("the worker needs object storage: set STORAGE_BACKEND=s3")
	}

	// SIGTERM/SIGINT start a graceful shutdown (see worker.Run).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
	queue, err := integrations.NewQueue(ctx, cfg)
	if err != nil {
		return err
	}
	if c, ok := queue.(io.Closer); ok {
		defer c.Close()
	}

	if err := os.MkdirAll(cfg.Worker.TempDir, 0o755); err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}

	w := worker.New(worker.Deps{
		Store:   jobs.NewStore(pool),
		Storage: storage,
		Queue:   queue,
		Media: &transcode.Tool{
			FFmpegPath:  cfg.Worker.FFmpegPath,
			FFprobePath: cfg.Worker.FFprobePath,
			Threads:     cfg.Worker.FFmpegThreads,
			Logger:      logger,
		},
		Config: cfg.Worker,
		Logger: logger,
	})
	return w.Run(ctx)
}
