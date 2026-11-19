package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// readCurrentTask returns the machine's current assigned task from
// current_task.txt next to the agent config: the first non-empty
// line, trimmed, capped at 200 runes. A missing or empty file means
// no task (the dashboard shows 待命). The file is re-read every
// report round so a task change takes effect within one interval
// without restarting the agent.
func readCurrentTask(cfgPath string) string {
	b, err := os.ReadFile(filepath.Join(filepath.Dir(cfgPath), "current_task.txt"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		r := []rune(line)
		if len(r) > 200 {
			line = string(r[:200])
		}
		return line
	}
	return ""
}

func runAgent(cfg AgentConfig, cfgPath string) {
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
		rep.Task = readCurrentTask(cfgPath)
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
