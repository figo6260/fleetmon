package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type machineState struct {
	Info      MachineInfo `json:"info"`
	Report    *Report     `json:"report,omitempty"`
	LastSeen  int64       `json:"last_seen"` // unix seconds of last report/probe
	Online    bool        `json:"online"`
	DownSince int64       `json:"down_since,omitempty"` // probe machines: when it went down
	Alert     bool        `json:"alert"`                // computed: offline > 5 min (always present; mailer depends on it)
}

type store struct {
	mu       sync.Mutex
	states   map[string]*machineState
	dataDir  string
	registry map[string]MachineInfo
}

func (s *store) stateFile() string { return filepath.Join(s.dataDir, "state.json") }

func (s *store) saveLocked() {
	out := map[string]*machineState{}
	for k, v := range s.states {
		out[k] = v
	}
	b, err := json.Marshal(out)
	if err == nil {
		_ = os.WriteFile(s.stateFile(), b, 0o644)
	}
}

func (s *store) load() {
	b, err := os.ReadFile(s.stateFile())
	if err != nil {
		return
	}
	var saved map[string]*machineState
	if json.Unmarshal(b, &saved) == nil {
		for k, v := range saved {
			s.states[k] = v
		}
	}
}

func (s *store) appendHistory(rep Report) {
	day := time.Now().Format("2006-01-02")
	f, err := os.OpenFile(filepath.Join(s.dataDir, "history-"+day+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(rep)
	f.Write(append(b, '\n'))
}

func (s *store) applyReport(rep Report) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[rep.Machine]
	if !ok {
		st = &machineState{Info: s.registry[rep.Machine]}
		s.states[rep.Machine] = st
	}
	r := rep
	st.Report = &r
	st.LastSeen = time.Now().Unix()
	st.Online = true
	s.saveLocked()
	s.appendHistory(rep)
}

// probeLoop does agentless external checks for registry entries with Probe set.
func (s *store) probeLoop() {
	client := &http.Client{Timeout: 8 * time.Second}
	for {
		for _, info := range s.registry {
			if info.Probe == "" {
				continue
			}
			// If a real agent on this machine reported recently, the probe
			// is only a fallback: skip so it does not clobber agent data.
			s.mu.Lock()
			if st, ok := s.states[info.ID]; ok && st.Report != nil && st.Report.Agent &&
				time.Now().Unix()-st.LastSeen <= 180 {
				s.mu.Unlock()
				continue
			}
			s.mu.Unlock()
			resp, err := client.Get(info.Probe)
			up := false
			detail := ""
			if err == nil {
				up = resp.StatusCode >= 200 && resp.StatusCode < 400
				detail = resp.Status
				resp.Body.Close()
			} else {
				detail = "unreachable"
			}
			s.mu.Lock()
			st, ok := s.states[info.ID]
			if !ok {
				st = &machineState{Info: info}
				s.states[info.ID] = st
			}
			rep := &Report{
				Machine: info.ID, Name: info.Name, Role: info.Role,
				Time: time.Now().Unix(), Agent: false,
				Services: []Service{{Name: "网站可达", Up: up, Detail: detail}},
			}
			st.Report = rep
			st.LastSeen = time.Now().Unix()
			st.Online = up
			if up {
				st.DownSince = 0
			} else if st.DownSince == 0 {
				st.DownSince = st.LastSeen
			}
			s.saveLocked()
			s.mu.Unlock()
		}
		time.Sleep(60 * time.Second)
	}
}

// pruneLoop deletes history files older than 14 days, checked daily.
func (s *store) pruneLoop() {
	for {
		entries, _ := os.ReadDir(s.dataDir)
		cutoff := time.Now().AddDate(0, 0, -14)
		for _, e := range entries {
			name := e.Name()
			if len(name) == len("history-2006-01-02.jsonl") && name[:8] == "history-" {
				if day, err := time.Parse("2006-01-02", name[8:18]); err == nil && day.Before(cutoff) {
					_ = os.Remove(filepath.Join(s.dataDir, name))
				}
			}
		}
		time.Sleep(24 * time.Hour)
	}
}

func runServer(cfg ServerConfig) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "data dir:", err)
		os.Exit(1)
	}
	registry := map[string]MachineInfo{}
	for _, m := range cfg.Machines {
		registry[m.ID] = m
	}
	s := &store{states: map[string]*machineState{}, dataDir: cfg.DataDir, registry: registry}
	for _, m := range cfg.Machines {
		s.states[m.ID] = &machineState{Info: m}
	}
	s.load()
	// after a restart, trust lastSeen from disk but recompute online below per request
	go s.probeLoop()
	go s.pruneLoop()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+cfg.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var rep Report
		if err := json.NewDecoder(r.Body).Decode(&rep); err != nil || rep.Machine == "" {
			http.Error(w, "bad report", http.StatusBadRequest)
			return
		}
		if _, ok := registry[rep.Machine]; !ok {
			http.Error(w, "unknown machine", http.StatusBadRequest)
			return
		}
		s.applyReport(rep)
		w.WriteHeader(http.StatusOK)
	})

	// Session cookie: HMAC-signed expiry, keyed by secrets already in config.
	// (HTTP Basic Auth proved unreliable on mobile Safari: the native prompt
	// loops / shows a blank page, so the web UI uses a form login + cookie.
	// Basic Auth stays as a fallback; Bearer token for automation.)
	base := cfg.WebBasePath // e.g. "/fleet" when a proxy strips that prefix
	cookiePath := base + "/"
	sessionKey := []byte(cfg.WebPassSHA + ":" + cfg.Token)
	makeSession := func() string {
		exp := strconv.FormatInt(time.Now().Add(30*24*time.Hour).Unix(), 10)
		mac := hmac.New(sha256.New, sessionKey)
		mac.Write([]byte("fleetmon-session:" + exp))
		return exp + "." + hex.EncodeToString(mac.Sum(nil))
	}
	sessionOK := func(r *http.Request) bool {
		c, err := r.Cookie("fleetmon_session")
		if err != nil {
			return false
		}
		parts := strings.SplitN(c.Value, ".", 2)
		if len(parts) != 2 {
			return false
		}
		exp, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || time.Now().Unix() > exp {
			return false
		}
		mac := hmac.New(sha256.New, sessionKey)
		mac.Write([]byte("fleetmon-session:" + parts[0]))
		want := hex.EncodeToString(mac.Sum(nil))
		return subtle.ConstantTimeCompare([]byte(want), []byte(parts[1])) == 1
	}

	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Automation (alert mailer, scripts) may use the agent token.
			if r.Header.Get("Authorization") == "Bearer "+cfg.Token {
				h(w, r)
				return
			}
			if sessionOK(r) {
				h(w, r)
				return
			}
			user, pass, ok := r.BasicAuth()
			sum := sha256.Sum256([]byte(pass))
			passOK := hex.EncodeToString(sum[:]) == cfg.WebPassSHA
			if ok && user == cfg.WebUser && passOK &&
				subtle.ConstantTimeCompare([]byte(user), []byte(cfg.WebUser)) == 1 {
				h(w, r)
				return
			}
			// Page loads go to the login form; API calls just get a 401.
			if r.URL.Path == "/" {
				http.Redirect(w, r, base+"/login", http.StatusFound)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
	}

	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			user := r.FormValue("user")
			pass := r.FormValue("pass")
			sum := sha256.Sum256([]byte(pass))
			passOK := hex.EncodeToString(sum[:]) == cfg.WebPassSHA
			if user == cfg.WebUser && passOK &&
				subtle.ConstantTimeCompare([]byte(user), []byte(cfg.WebUser)) == 1 {
				http.SetCookie(w, &http.Cookie{
					Name:     "fleetmon_session",
					Value:    makeSession(),
					Path:     cookiePath,
					MaxAge:   30 * 24 * 3600,
					HttpOnly: true,
					Secure:   true,
					SameSite: http.SameSiteLaxMode,
				})
				http.Redirect(w, r, base+"/", http.StatusFound)
				return
			}
			http.Redirect(w, r, base+"/login?e=1", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		errMsg := ""
		if r.URL.Query().Get("e") == "1" {
			errMsg = `<div class="err">用户名或密码不对，请再试一次。</div>`
		}
		fmt.Fprint(w, strings.Replace(loginHTML, "{{ERR}}", errMsg, 1))
	})

	mux.HandleFunc("/logout", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     "fleetmon_session",
			Value:    "",
			Path:     cookiePath,
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, base+"/login", http.StatusFound)
	})

	mux.HandleFunc("/", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, dashboardHTML)
	}))

	mux.HandleFunc("/api/state", auth(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		now := time.Now().Unix()
		out := make([]*machineState, 0, len(cfg.Machines))
		for _, m := range cfg.Machines {
			st := s.states[m.ID]
			if st == nil {
				st = &machineState{Info: m}
			}
			cp := *st
			if cp.Report != nil && cp.Report.Agent {
				cp.Online = now-cp.LastSeen <= 180
				cp.Alert = now-cp.LastSeen > 300
			} else if cp.Report != nil && !cp.Report.Agent {
				cp.Alert = cp.DownSince > 0 && now-cp.DownSince > 300
			}
			out = append(out, &cp)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"now": now, "machines": out})
	}))

	fmt.Println("fleetmon server listening on", cfg.Addr, "data:", cfg.DataDir)
	srv := &http.Server{Addr: cfg.Addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
}
