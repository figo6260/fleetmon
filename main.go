package main

import (
	"encoding/json"
	"fmt"
	"os"
)

func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: fleetmon <agent|server> <config.json>")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "agent":
		var cfg AgentConfig
		if err := loadJSON(os.Args[2], &cfg); err != nil {
			fmt.Fprintln(os.Stderr, "agent config:", err)
			os.Exit(1)
		}
		runAgent(cfg)
	case "server":
		var cfg ServerConfig
		if err := loadJSON(os.Args[2], &cfg); err != nil {
			fmt.Fprintln(os.Stderr, "server config:", err)
			os.Exit(1)
		}
		runServer(cfg)
	default:
		fmt.Fprintln(os.Stderr, "unknown mode:", os.Args[1])
		os.Exit(2)
	}
}
