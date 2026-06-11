package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xJogger/fake-komga-115/internal/app"
)

func main() {
	var (
		host    = flag.String("host", envOr("FK115_HOST", "0.0.0.0"), "listen host")
		port    = flag.Int("port", envInt("FK115_PORT", 25600), "listen port")
		dataDir = flag.String("data-dir", envOr("FK115_DATA_DIR", "./data"), "data directory")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, app.Options{
		Host:    *host,
		Port:    *port,
		DataDir: *dataDir,
		Logger:  logger,
	})
	if err != nil {
		logger.Error("initialize application", "error", err)
		os.Exit(1)
	}
	defer a.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- a.Run() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := a.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown", "error", err)
		}
	case err := <-errCh:
		if err != nil {
			logger.Error("server stopped", "error", err)
			os.Exit(1)
		}
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	var value int
	if _, err := fmt.Sscanf(os.Getenv(key), "%d", &value); err == nil && value > 0 {
		return value
	}
	return fallback
}
