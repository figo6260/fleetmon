//go:build windows

package main

import (
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Windows collector uses PowerShell CIM queries; good enough for one laptop.

func ps(script string) string {
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func psFloat(script string) float64 {
	v, _ := strconv.ParseFloat(ps(script), 64)
	return v
}

func checkService(c ServiceCheck) Service {
	s := Service{Name: c.Name}
	switch c.Kind {
	case "process":
		out := ps(fmt.Sprintf("(Get-Process -Name '%s' -ErrorAction SilentlyContinue | Measure-Object).Count", c.Match))
		n, _ := strconv.Atoi(out)
		if n == 0 { // match by path substring via CIM
			out = ps(fmt.Sprintf("(Get-CimInstance Win32_Process -Filter \"Name like '%%%s%%'\" | Measure-Object).Count", c.Match))
			n, _ = strconv.Atoi(out)
		}
		s.Up = n > 0
	case "port":
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", c.Port), 2*time.Second)
		s.Up = err == nil
		if err == nil {
			conn.Close()
		}
	case "http":
		expect := c.Expect
		if expect == 0 {
			expect = 200
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(c.URL)
		if err == nil {
			resp.Body.Close()
			s.Up = resp.StatusCode == expect
			s.Detail = resp.Status
		}
	case "systemd": // on Windows this means a Windows service name
		out := ps(fmt.Sprintf("(Get-Service -Name '%s' -ErrorAction SilentlyContinue).Status", c.Unit))
		s.Up = out == "Running"
	}
	return s
}

func collect(cfg AgentConfig) Report {
	rep := Report{Time: time.Now().Unix()}

	// CPU: sample TotalProcessorTime deltas over ~1s, plus overall load.
	type pinfo struct {
		name string
		cpu  time.Duration
		mem  int64
	}
	snap := func() map[int]pinfo {
		m := map[int]pinfo{}
		out := ps("Get-Process | ForEach-Object { \"$($_.Id)|$($_.ProcessName)|$($_.CPU)|$($_.WorkingSet64)\" }")
		for _, line := range strings.Split(out, "\n") {
			f := strings.Split(strings.TrimSpace(line), "|")
			if len(f) != 4 {
				continue
			}
			pid, _ := strconv.Atoi(f[0])
			cpuSec, _ := strconv.ParseFloat(f[2], 64)
			mem, _ := strconv.ParseInt(f[3], 10, 64)
			m[pid] = pinfo{name: f[1], cpu: time.Duration(cpuSec * float64(time.Second)), mem: mem}
		}
		return m
	}
	load0 := psFloat("(Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average")
	s0 := snap()
	start := time.Now()
	time.Sleep(800 * time.Millisecond)
	s1 := snap()
	elapsed := time.Since(start)
	_ = load0

	numCPU := psFloat("$env:NUMBER_OF_PROCESSORS")
	if numCPU <= 0 {
		numCPU = 1
	}
	rep.MemTotal = uint64(psFloat("(Get-CimInstance Win32_OperatingSystem).TotalVisibleMemorySize") * 1024)
	freeMem := uint64(psFloat("(Get-CimInstance Win32_OperatingSystem).FreePhysicalMemory") * 1024)
	if rep.MemTotal > 0 {
		rep.MemUsed = rep.MemTotal - freeMem
	}

	var tops []Proc
	var totalBusy time.Duration
	for pid, p1 := range s1 {
		if p0, ok := s0[pid]; ok {
			d := p1.cpu - p0.cpu
			totalBusy += d
			cpuPct := 100 * float64(d) / float64(elapsed) / numCPU
			var memPct float64
			if rep.MemTotal > 0 {
				memPct = 100 * float64(p1.mem) / float64(rep.MemTotal)
			}
			if cpuPct >= 0.1 || memPct >= 1 {
				tops = append(tops, Proc{Name: p1.name, CPU: cpuPct, Mem: memPct})
			}
		}
	}
	rep.CPU = 100 * float64(totalBusy) / float64(elapsed) / numCPU
	for i := 0; i < len(tops); i++ {
		for j := i + 1; j < len(tops); j++ {
			if tops[j].CPU > tops[i].CPU {
				tops[i], tops[j] = tops[j], tops[i]
			}
		}
	}
	if len(tops) > 5 {
		tops = tops[:5]
	}
	rep.Top = tops

	// disks: fixed local drives
	out := ps("Get-CimInstance Win32_LogicalDisk -Filter \"DriveType=3\" | ForEach-Object { \"$($_.DeviceID)|$($_.Size)|$($_.FreeSpace)\" }")
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), "|")
		if len(f) != 3 {
			continue
		}
		total, _ := strconv.ParseUint(f[1], 10, 64)
		free, _ := strconv.ParseUint(f[2], 10, 64)
		if total > 0 {
			rep.Disks = append(rep.Disks, Disk{Mount: f[0], Total: total, Free: free})
		}
	}

	for _, c := range cfg.Services {
		rep.Services = append(rep.Services, checkService(c))
	}

	rep.Uptime = uint64(psFloat("[int]((Get-Date) - (Get-CimInstance Win32_OperatingSystem).LastBootUpTime).TotalSeconds"))
	return rep
}
