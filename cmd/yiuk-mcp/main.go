package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"yiuk-switchboard/internal/agenttools"
	"yiuk-switchboard/internal/localbridge"
	"yiuk-switchboard/internal/mcpadapter"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	var backend agenttools.Backend
	local, err := localbridge.NewFromUserConfig()
	if err == nil {
		backend = local
	}
	if err := mcpadapter.Run(ctx, agenttools.NewCore(backend)); err != nil {
		log.Print(err)
	}
}
