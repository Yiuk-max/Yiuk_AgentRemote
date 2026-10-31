package main

import (
	"context"
	"log"
	"log/slog"
	"os/signal"
	"syscall"

	"agentremote/internal/agenttools"
	"agentremote/internal/localbridge"
	"agentremote/internal/mcpadapter"
	"agentremote/internal/platformclient"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var backend agenttools.Backend
	platform, platformErr := platformclient.NewFromUserConfig()
	if platformErr == nil {
		slog.Info("MCP backend: remote platform")
		backend = platform
	} else if local, err := localbridge.NewFromUserConfig(); err == nil {
		slog.Info("MCP backend: local Qt bridge")
		backend = local
	} else {
		// 明确显示平台配置被拒绝的原因，而不只是报告后端未配置
		slog.Warn("MCP backend is not configured; tools will return NOT_CONFIGURED",
			"platform_config_error", platformErr)
	}

	if err := mcpadapter.Run(ctx, agenttools.NewCore(backend)); err != nil {
		log.Print(err)
	}
}
