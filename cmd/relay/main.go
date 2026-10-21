// Command relay runs the legacy Minecraft TCP relay. It is kept separate from
// the remote-assist platform listener so the two business types have
// independent ports, credentials and resource limits.
package main

import (
	"flag"
	"fmt"
	"os"

	"yiuk-switchboard/internal/server"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the Minecraft relay YAML configuration")
	flag.Parse()
	if err := server.RunFromConfig(*configPath); err != nil {
		fmt.Fprintln(os.Stderr, "relay error:", err)
		os.Exit(1)
	}
}
