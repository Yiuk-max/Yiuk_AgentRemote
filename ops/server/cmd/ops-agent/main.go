package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"yiuk-switchboard/ops/server/internal/agent"
)

func main() {
	path := flag.String("config", "ops-agent.yaml", "path to ops agent YAML config")
	flag.Parse()
	cfg, err := agent.LoadConfig(*path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := agent.Run(cfg); err != nil {
		log.Fatal(err)
	}
}
