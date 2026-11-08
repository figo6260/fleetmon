//go:build linux

package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type cpuTimes struct {
	total, idle uint64
}

func readCPUTimes() cpuTimes {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "cpu ") {
			f := strings.Fields(line)
			var total, idle uint64
			for i, s := range f[1:] {
				v, _ := strconv.ParseUint(s, 10, 64)
				total += v
				if i == 3 || i == 4 { // idle + iowait
					idle += v
				}
			}
			return cpuTimes{total, idle}
		}
	}
	return cpuTimes{}
}

type procSample struct {
	pid       int
	name      string
	tick      uint64
	rss       uint64
	startTick uint64 // field 22 of /proc/pid/stat: start time in clock ticks since boot
	cmd       string // short command line ("" for kernel threads)
}

func truncateCmd(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func readProcs() []procSample {
	entries, _ := filepath.Glob("/proc/[0-9]*")
	var out []procSample
	for _, e := range entries {
		stat, err := os.ReadFile(filepath.Join(e, "stat"))
		if err != nil {
			continue
		}
		s := string(stat)
		open := strings.Index(s, "(")
		closeIdx := strings.LastIndex(s, ")")
		if open < 0 || closeIdx < 0 {
			continue
		}
		name := s[open+1 : closeIdx]
		f := strings.Fields(s[closeIdx+2:])
		// f[0]=state; utime is field 14 overall -> index 11 here; stime field 15 -> 12; rss field 24 -> 21 (in pages); starttime field 22 -> 19
		if len(f) < 22 {
			continue
		}
		utime, _ := strconv.ParseUint(f[11], 10, 64)
		stime, _ := strconv.ParseUint(f[12], 10, 64)
		rssPages, _ := strconv.ParseUint(f[21], 10, 64)
		startTick, _ := strconv.ParseUint(f[19], 10, 64)
		pid, _ := strconv.Atoi(filepath.Base(e))
		cmd := ""
		if b, err := os.ReadFile(filepath.Join(e, "cmdline")); err == nil && len(b) > 0 {
			cmd = truncateCmd(strings.ReplaceAll(string(b), "\x00", " "), 160)
		}
		out = append(out, procSample{pid: pid, name: name, tick: utime + stime, rss: rssPages * 4096, startTick: startTick, cmd: cmd})
	}
	return out
}

func readUptimeSec() uint64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(f[0], 64)
	if v < 0 {
		return 0
	}
	return uint64(v)
}

func readMem() (used, total uint64) {
	b, _ := os.ReadFile("/proc/meminfo")
	var avail uint64
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			avail = v * 1024
		}
	}
	if total > 0 && avail <= total {
		used = total - avail
	}
	return used, total
}

func diskStat(mount string) (Disk, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(mount, &st); err != nil {
		return Disk{}, false
	}
	total := st.Blocks * uint64(st.Bsize)
	free := st.Bavail * uint64(st.Bsize)
	return Disk{Mount: mount, Total: total, Free: free}, true
}

func checkService(c ServiceCheck) Service {
	s := Service{Name: c.Name}
	switch c.Kind {
	case "process":
		found := false
		entries, _ := filepath.Glob("/proc/[0-9]*/comm")
		for _, e := range entries {
			b, _ := os.ReadFile(e)
			if strings.Contains(strings.TrimSpace(string(b)), c.Match) {
				found = true
				break
			}
		}
		if !found { // fall back to cmdline (scripts have comm=interpreter)
			entries, _ := filepath.Glob("/proc/[0-9]*/cmdline")
			for _, e := range entries {
				b, _ := os.ReadFile(e)
				if strings.Contains(string(b), c.Match) {
					found = true
					break
				}
			}
		}
		s.Up = found
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
	case "systemd":
		out, err := exec.Command("systemctl", "is-active", c.Unit).Output()
		s.Up = err == nil && strings.TrimSpace(string(out)) == "active"
	}
	return s
}

func collect(cfg AgentConfig) Report {
	t0 := readCPUTimes()
	p0 := readProcs()
	time.Sleep(700 * time.Millisecond)
	t1 := readCPUTimes()
	p1 := readProcs()

	rep := Report{Time: time.Now().Unix()}
	totalDelta := t1.total - t0.total
	if totalDelta > 0 {
		rep.CPU = 100 * float64(totalDelta-(t1.idle-t0.idle)) / float64(totalDelta)
	}
	rep.MemUsed, rep.MemTotal = readMem()

	before := map[int]procSample{}
	for _, p := range p0 {
		before[p.pid] = p
	}
	uptime := readUptimeSec()
	const clockTicks = 100 // USER_HZ on all our kernels
	var tops []Proc
	var recent []ProcDetail
	for _, p := range p1 {
		if b, ok := before[p.pid]; ok && totalDelta > 0 {
			cpu := 100 * float64(p.tick-b.tick) / float64(totalDelta)
			var mem float64
			if rep.MemTotal > 0 {
				mem = 100 * float64(p.rss) / float64(rep.MemTotal)
			}
			if cpu >= 0.1 || mem >= 1 {
				tops = append(tops, Proc{Name: p.name, CPU: cpu, Mem: mem})
			}
			// recently started = a "task" the user wants to watch
			// (installers, builds, code runs, ...)
			if uptime > 0 && p.startTick > 0 {
				startSec := p.startTick / clockTicks
				if startSec <= uptime {
					age := uptime - startSec
					if age <= 900 {
					recent = append(recent, ProcDetail{
						Name: p.name, PID: p.pid, CPU: cpu, Mem: mem,
						Cmd: p.cmd, Age: age,
					})
				}
			}
		}
	}
	}
	sort.Slice(tops, func(i, j int) bool { return tops[i].CPU > tops[j].CPU })
	if len(tops) > 12 {
		tops = tops[:12]
	}
	rep.Top = tops
	sort.Slice(recent, func(i, j int) bool { return recent[i].Age < recent[j].Age })
	if len(recent) > 12 {
		recent = recent[:12]
	}
	rep.Recent = recent

	seen := map[string]bool{}
	addDisk := func(m string) {
		if seen[m] {
			return
		}
		seen[m] = true
		if d, ok := diskStat(m); ok && d.Total > 0 {
			rep.Disks = append(rep.Disks, d)
		}
	}
	addDisk("/")
	for _, m := range cfg.Mounts {
		addDisk(m)
	}

	for _, c := range cfg.Services {
		rep.Services = append(rep.Services, checkService(c))
	}

	if b, err := os.ReadFile("/proc/uptime"); err == nil {
		f := strings.Fields(string(b))
		if len(f) > 0 {
			v, _ := strconv.ParseFloat(f[0], 64)
			rep.Uptime = uint64(v)
		}
	}
	return rep
}
