// Command platform runs the AgentRemote remote-assist platform server.
//
// The server is intentionally in-memory: restarting it invalidates every
// device password, connection, session and task.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"agentremote/internal/platform/config"
	"agentremote/internal/platform/core"
	"agentremote/internal/platform/gateway"
	"agentremote/internal/platform/logging"
)

func main() {
	configPath := flag.String("config", "platform.yaml", "path to the platform YAML configuration")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "platform config error:", err)
		os.Exit(1)
	}

	closeLog, logger, err := logging.Setup(cfg.LogFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "platform log error:", err)
		os.Exit(1)
	}
	defer closeLog()
	slog.SetDefault(logger)

	gw, err := gateway.New(cfg, logger)
	if err != nil {
		logger.Error("could not start platform listener", "error", err)
		os.Exit(1)
	}
	coreSvc := core.New(cfg, logger, gw)
	gw.Bind(coreSvc)

	if len(cfg.PlayerPassword) < config.RecommendedPlayerPasswordLen {
		logger.Warn("player_password is shorter than recommended; use a longer random secret for public deployments",
			"length", len(cfg.PlayerPassword), "recommended", config.RecommendedPlayerPasswordLen)
	}
	logger.Info("platform server listening", "listen", cfg.Listen, "tls", cfg.TLSCert != "", "insecure", cfg.AllowInsecure)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM) //优雅退出，context程序退出
	defer stop()

	go func() { _ = coreSvc.Run(ctx) }()
	if err := gw.Run(ctx); err != nil { //开始运行服务器
		logger.Error("platform server stopped", "error", err)
		os.Exit(1)
	}
	logger.Info("platform server stopped")
}
