package main

import (
	"context"
	"log"
	"log/slog"
	"os/signal"
	"syscall"

	"yiuk-switchboard/internal/agenttools"
	"yiuk-switchboard/internal/localbridge"
	"yiuk-switchboard/internal/mcpadapter"
	"yiuk-switchboard/internal/platformclient"
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
		// Surface why the platform configuration was rejected instead of only
		// reporting an unconfigured backend.
		slog.Warn("MCP backend is not configured; tools will return NOT_CONFIGURED",
			"platform_config_error", platformErr)
	}

	if err := mcpadapter.Run(ctx, agenttools.NewCore(backend)); err != nil {
		log.Print(err)
	}
}
