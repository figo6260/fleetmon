package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"
)

func runAgent(cfg AgentConfig) {
	if cfg.Interval <= 0 {
		cfg.Interval = 60
	}
	hostname, _ := os.Hostname()
	client := &http.Client{Timeout: 15 * time.Second}
	fmt.Printf("fleetmon agent %s (%s) -> %s every %ds\n", cfg.Machine, cfg.Name, cfg.Server, cfg.Interval)
	for {
		rep := collect(cfg)
		rep.Machine = cfg.Machine
		rep.Name = cfg.Name
		rep.Role = cfg.Role
		rep.Hostname = hostname
		rep.OS = runtime.GOOS
		rep.Agent = true
		body, _ := json.Marshal(rep)
		req, err := http.NewRequest(http.MethodPost, cfg.Server+"/api/report", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
			resp, err := client.Do(req)
			if err != nil {
				fmt.Println("report failed:", err)
			} else {
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					fmt.Println("report status:", resp.Status)
				}
			}
		}
		time.Sleep(time.Duration(cfg.Interval) * time.Second)
	}
}
