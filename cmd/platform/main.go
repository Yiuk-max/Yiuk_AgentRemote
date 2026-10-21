// Command platform runs the Yiuk Switchboard remote-assist platform server.
//
// The server is intentionally in-memory: restarting it invalidates every
// device password, connection, session and task. Minecraft relay traffic is
// served by the separate relay process so the two business types keep
// independent ports, credentials and limits.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"yiuk-switchboard/internal/platform/config"
	"yiuk-switchboard/internal/platform/server"
)

func main() {
	configPath := flag.String("config", "platform.yaml", "path to the platform YAML configuration")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "platform config error:", err)
		os.Exit(1)
	}

	closeLog, logger, err := setupLogging(cfg.LogFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "platform log error:", err)
		os.Exit(1)
	}
	defer closeLog()
	slog.SetDefault(logger)

	srv, err := server.New(cfg, logger)
	if err != nil {
		logger.Error("could not start platform listener", "error", err)
		os.Exit(1)
	}
	if len(cfg.PlayerPassword) < config.RecommendedPlayerPasswordLen {
		logger.Warn("player_password is shorter than recommended; use a longer random secret for public deployments",
			"length", len(cfg.PlayerPassword), "recommended", config.RecommendedPlayerPasswordLen)
	}
	logger.Info("platform server listening", "listen", cfg.Listen, "tls", cfg.TLSCert != "", "insecure", cfg.AllowInsecure)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := srv.Run(ctx); err != nil {
		logger.Error("platform server stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("platform server stopped")
}

func setupLogging(path string) (func(), *slog.Logger, error) {
	handler := slog.NewJSONHandler(os.Stderr, nil)
	if path == "" {
		return func() {}, slog.New(handler), nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, nil, err
	}
	handler = slog.NewJSONHandler(file, nil)
	closeFn := func() {
		if err := file.Sync(); err != nil {
			fmt.Fprintln(os.Stderr, "platform log sync error:", err)
		}
		_ = file.Close()
	}
	return closeFn, slog.New(handler), nil
}
