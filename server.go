package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
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

// ---- login failure lockout ----
//
// The dashboard login is the only password surface exposed to the
// public internet, so failed logins are counted per source IP: too
// many failures inside a rolling window lock that IP out for a fixed
// period. While locked, every login from that IP is refused — even
// with the correct password. A successful login clears the counter.
// Counts live in memory only (a restart resets them), and locks for
// different IPs are fully independent. Already-established sessions
// and Bearer-token automation are never affected: the guard only sits
// on the credential-checking paths (login form, HTTP Basic fallback).

type ipLoginState struct {
	fails       []time.Time // failure timestamps inside the rolling window
	lockedUntil time.Time
	lastLockLog time.Time
}

type loginGuard struct {
	mu        sync.Mutex
	threshold int           // failures inside window that trigger a lock
	window    time.Duration // rolling failure-counting window
	lockDur   time.Duration // how long a lock lasts
	byIP      map[string]*ipLoginState
}

func envIntOr(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

// newLoginGuard builds the guard with production defaults: 5 failures
// within 10 minutes lock the source IP for 30 minutes. The env
// overrides exist so sandbox tests can shorten the durations; the
// production service sets none of them.
func newLoginGuard() *loginGuard {
	return &loginGuard{
		threshold: envIntOr("FLEETMON_LOGIN_LOCK_THRESHOLD", 5),
		window:    time.Duration(envIntOr("FLEETMON_LOGIN_LOCK_WINDOW_SEC", 600)) * time.Second,
		lockDur:   time.Duration(envIntOr("FLEETMON_LOGIN_LOCK_SEC", 1800)) * time.Second,
		byIP:      map[string]*ipLoginState{},
	}
}

// clientIP returns the address a login attempt is accounted to.
//
// In production the public path is: visitor -> la-hub nginx ->
// reverse SSH tunnel -> this server, so requests that arrive via the
// tunnel have a loopback RemoteAddr and carry the visitor's real
// address in X-Real-IP, which la-hub's nginx force-sets from its own
// peer address (a visitor cannot spoof it — nginx overwrites the
// header; X-Forwarded-For is NOT trusted for the same reason, the
// /fleet/ location does not set it). X-Real-IP is therefore honored
// only when the TCP peer is loopback, i.e. the request really came
// through the local tunnel endpoint. Any direct connection (mesh or
// public) is accounted to its true RemoteAddr and its headers are
// ignored.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "127.0.0.1" || host == "::1" {
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
			return ip.String()
		}
	}
	return host
}

// locked reports whether ip is currently locked out and, if so, how
// much lock time remains.
func (g *loginGuard) locked(ip string) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.byIP[ip]
	if !ok {
		return 0, false
	}
	if rem := time.Until(st.lockedUntil); rem > 0 {
		return rem, true
	}
	return 0, false
}

// noteLockedReject writes an audit line for a login rejected because
// of an active lock, rate-limited to one line per minute per IP so a
// persistent bot cannot flood the log.
func (g *loginGuard) noteLockedReject(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st, ok := g.byIP[ip]
	if !ok {
		return
	}
	now := time.Now()
	if now.Sub(st.lastLockLog) >= time.Minute {
		st.lastLockLog = now
		fmt.Fprintf(os.Stderr, "%s login-locked-reject: ip=%s locked_until=%s\n",
			now.Format(time.RFC3339), ip, st.lockedUntil.Format(time.RFC3339))
	}
}

// fail records one failed login from ip. It returns true when the IP
// is locked after this failure (either it just tripped the threshold
// or it was already locked).
func (g *loginGuard) fail(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	st, ok := g.byIP[ip]
	if !ok {
		st = &ipLoginState{}
		g.byIP[ip] = st
	}
	if now.Before(st.lockedUntil) {
		return true // already locked; further hits do not extend it
	}
	cutoff := now.Add(-g.window)
	kept := st.fails[:0]
	for _, t := range st.fails {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	st.fails = append(kept, now)
	if len(st.fails) >= g.threshold {
		st.lockedUntil = now.Add(g.lockDur)
		st.fails = nil
		fmt.Fprintf(os.Stderr, "%s login-lockout: ip=%s locked_for=%s after %d failures in %s\n",
			now.Format(time.RFC3339), ip, g.lockDur, g.threshold, g.window)
		return true
	}
	fmt.Fprintf(os.Stderr, "%s login-fail: ip=%s failures_in_window=%d\n",
		now.Format(time.RFC3339), ip, len(st.fails))
	return false
}

// success clears ip's failure record after a good login.
func (g *loginGuard) success(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if st, ok := g.byIP[ip]; ok && len(st.fails) > 0 {
		fmt.Fprintf(os.Stderr, "%s login-success: ip=%s failure count cleared\n",
			time.Now().Format(time.RFC3339), ip)
	}
	delete(g.byIP, ip)
}

func runServer(cfg ServerConfig, cfgPath string) {
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
	guard := newLoginGuard()
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

	// The web password can be changed at runtime (POST /api/password), so
	// the current hash lives in a mutable cell rather than only in cfg;
	// successful changes are also persisted back to cfgPath.
	var passMu sync.RWMutex
	curPassSHA := cfg.WebPassSHA
	getPassSHA := func() string {
		passMu.RLock()
		defer passMu.RUnlock()
		return curPassSHA
	}
	setPassSHA := func(sha string) {
		passMu.Lock()
		defer passMu.Unlock()
		curPassSHA = sha
	}
	// sessionKey derives from the password hash, so a password change
	// invalidates every outstanding session cookie automatically.
	sessionKey := func() []byte { return []byte(getPassSHA() + ":" + cfg.Token) }
	makeSession := func() string {
		exp := strconv.FormatInt(time.Now().Add(30*24*time.Hour).Unix(), 10)
		mac := hmac.New(sha256.New, sessionKey())
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
		mac := hmac.New(sha256.New, sessionKey())
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
			if ok {
				// Credential attempt (as opposed to an anonymous page
				// load): subject to the per-IP failure lockout.
				ip := clientIP(r)
				if _, isLocked := guard.locked(ip); isLocked {
					guard.noteLockedReject(ip)
					http.Error(w, "too many failed login attempts", http.StatusTooManyRequests)
					return
				}
				sum := sha256.Sum256([]byte(pass))
				passOK := hex.EncodeToString(sum[:]) == getPassSHA()
				if user == cfg.WebUser && passOK &&
					subtle.ConstantTimeCompare([]byte(user), []byte(cfg.WebUser)) == 1 {
					guard.success(ip)
					h(w, r)
					return
				}
				guard.fail(ip)
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
			ip := clientIP(r)
			if rem, isLocked := guard.locked(ip); isLocked {
				// Locked out: refuse without even checking the
				// password, so the lock cannot be bypassed by
				// guessing right during the lock period.
				guard.noteLockedReject(ip)
				mins := int((rem + time.Minute - 1) / time.Minute)
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(http.StatusTooManyRequests)
				msg := fmt.Sprintf(`<div class="err">这个来源刚才输错密码太多次，已被临时锁定，请约 %d 分钟后再试。</div>`, mins)
				fmt.Fprint(w, strings.Replace(loginHTML, "{{ERR}}", msg, 1))
				return
			}
			user := r.FormValue("user")
			pass := r.FormValue("pass")
			sum := sha256.Sum256([]byte(pass))
			passOK := hex.EncodeToString(sum[:]) == getPassSHA()
			if user == cfg.WebUser && passOK &&
				subtle.ConstantTimeCompare([]byte(user), []byte(cfg.WebUser)) == 1 {
				guard.success(ip)
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
			guard.fail(ip)
			http.Redirect(w, r, base+"/login?e=1", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		errMsg := ""
		if r.URL.Query().Get("e") == "1" {
			errMsg = `<div class="err">用户名或密码不对，请再试一次。</div>`
		}
		if r.URL.Query().Get("e") == "2" {
			errMsg = `<div class="err">这个来源刚才输错密码太多次，已被临时锁定，请稍后再试。</div>`
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

	// Config write-back (password change, machine rename) goes through
	// cfgWriteMu so two writers cannot interleave read-modify-write
	// cycles on the config file.
	var cfgWriteMu sync.Mutex

	// writeConfigRaw atomically replaces the config file with raw
	// (temp file + rename, 0600 like the original).
	writeConfigRaw := func(raw map[string]json.RawMessage) error {
		out, err := json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
		out = append(out, '\n')
		tmp, err := os.CreateTemp(filepath.Dir(cfgPath), ".server.json.tmp-*")
		if err != nil {
			return err
		}
		tmpName := tmp.Name()
		defer os.Remove(tmpName)
		if err := tmp.Chmod(0o600); err != nil {
			tmp.Close()
			return err
		}
		if _, err := tmp.Write(out); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		return os.Rename(tmpName, cfgPath)
	}

	// loadConfigRaw decodes the config file into raw JSON values so a
	// single field can be changed while every other field (machines,
	// token, ...) is preserved.
	loadConfigRaw := func() (map[string]json.RawMessage, error) {
		b, err := os.ReadFile(cfgPath)
		if err != nil {
			return nil, err
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(b, &raw); err != nil {
			return nil, err
		}
		return raw, nil
	}

	// persistPassSHA writes a new web_pass_sha256 back to the config file
	// this process was started with. Only that one field changes.
	persistPassSHA := func(newSHA string) error {
		cfgWriteMu.Lock()
		defer cfgWriteMu.Unlock()
		raw, err := loadConfigRaw()
		if err != nil {
			return err
		}
		v, err := json.Marshal(newSHA)
		if err != nil {
			return err
		}
		raw["web_pass_sha256"] = v
		return writeConfigRaw(raw)
	}

	// persistMachineRename writes the display_name / desc override for
	// one machine back to the config file. Only that machine's entry
	// changes; entries are matched by id and edited as raw JSON so all
	// other fields and entries are preserved. A nil desc leaves the
	// stored desc untouched; an empty desc removes the override.
	persistMachineRename := func(id, displayName string, desc *string) error {
		cfgWriteMu.Lock()
		defer cfgWriteMu.Unlock()
		raw, err := loadConfigRaw()
		if err != nil {
			return err
		}
		var entries []json.RawMessage
		if err := json.Unmarshal(raw["machines"], &entries); err != nil {
			return err
		}
		found := false
		for i, e := range entries {
			var probe struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(e, &probe); err != nil || probe.ID != id {
				continue
			}
			var entry map[string]json.RawMessage
			if err := json.Unmarshal(e, &entry); err != nil {
				return err
			}
			v, err := json.Marshal(displayName)
			if err != nil {
				return err
			}
			entry["display_name"] = v
			if desc != nil {
				if *desc == "" {
					delete(entry, "desc")
				} else {
					dv, err := json.Marshal(*desc)
					if err != nil {
						return err
					}
					entry["desc"] = dv
				}
			}
			nb, err := json.Marshal(entry)
			if err != nil {
				return err
			}
			entries[i] = nb
			found = true
			break
		}
		if !found {
			return fmt.Errorf("machine %s not in config", id)
		}
		mb, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		raw["machines"] = mb
		return writeConfigRaw(raw)
	}

	mux.HandleFunc("/api/password", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Current string `json:"current_password"`
			New     string `json:"new_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "请求格式不对", http.StatusBadRequest)
			return
		}
		sum := sha256.Sum256([]byte(req.Current))
		curOK := subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(getPassSHA())) == 1
		if !curOK {
			http.Error(w, "当前密码不对", http.StatusUnauthorized)
			return
		}
		if n := len([]rune(req.New)); n < 8 || n > 64 {
			http.Error(w, "新密码长度要在 8 到 64 位之间", http.StatusBadRequest)
			return
		}
		nsum := sha256.Sum256([]byte(req.New))
		newSHA := hex.EncodeToString(nsum[:])
		if err := persistPassSHA(newSHA); err != nil {
			fmt.Fprintln(os.Stderr, "persist password:", err)
			http.Error(w, "保存新密码失败", http.StatusInternalServerError)
			return
		}
		setPassSHA(newSHA)
		// The session key derives from the password hash, so every old
		// session (including this request's cookie) is already invalid;
		// clear the cookie explicitly too, and let the UI re-login.
		http.SetCookie(w, &http.Cookie{
			Name:     "fleetmon_session",
			Value:    "",
			Path:     cookiePath,
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteLaxMode,
		})
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true}`)
	}))

	// Rename changes only the dashboard display layer: a display_name /
	// desc override stored on the machine's registry entry in the config
	// file. The machine id (the agent's identity key) and the agent's
	// own config are never touched, and the change takes effect in
	// /api/state immediately without a restart.
	mux.HandleFunc("/api/machines/", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/api/machines/")
		if !strings.HasSuffix(rest, "/rename") {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimSuffix(rest, "/rename")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			DisplayName *string `json:"display_name"`
			Description *string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "请求格式不对", http.StatusBadRequest)
			return
		}
		badText := func(s string) string {
			for _, ch := range s {
				if unicode.IsControl(ch) || ch == '<' || ch == '>' {
					return "不能包含控制字符或尖括号"
				}
			}
			return ""
		}
		if req.DisplayName == nil {
			http.Error(w, "显示名必填", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(*req.DisplayName)
		if name == "" {
			http.Error(w, "显示名不能为空", http.StatusBadRequest)
			return
		}
		if len([]rune(name)) > 40 {
			http.Error(w, "显示名最长 40 个字符", http.StatusBadRequest)
			return
		}
		if why := badText(name); why != "" {
			http.Error(w, "显示名"+why, http.StatusBadRequest)
			return
		}
		var desc *string
		if req.Description != nil {
			d := strings.TrimSpace(*req.Description)
			if len([]rune(d)) > 80 {
				http.Error(w, "描述最长 80 个字符", http.StatusBadRequest)
				return
			}
			if why := badText(d); why != "" {
				http.Error(w, "描述"+why, http.StatusBadRequest)
				return
			}
			desc = &d
		}
		s.mu.Lock()
		info, ok := s.registry[id]
		s.mu.Unlock()
		if !ok {
			http.Error(w, "没有这台机器", http.StatusNotFound)
			return
		}
		oldName, oldDesc := info.DisplayName, info.Desc
		if err := persistMachineRename(id, name, desc); err != nil {
			fmt.Fprintln(os.Stderr, "persist rename:", err)
			http.Error(w, "保存新名字失败", http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		info.DisplayName = name
		if desc != nil {
			info.Desc = *desc
		}
		s.registry[id] = info
		if st, ok := s.states[id]; ok {
			st.Info = info
		}
		for i := range cfg.Machines {
			if cfg.Machines[i].ID == id {
				cfg.Machines[i] = info
			}
		}
		s.saveLocked()
		s.mu.Unlock()
		fmt.Fprintf(os.Stderr, "%s rename: user=%s machine=%s display_name %q -> %q desc %q -> %q\n",
			time.Now().Format(time.RFC3339), cfg.WebUser, id, oldName, name, oldDesc, info.Desc)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "id": id, "display_name": name, "description": info.Desc,
		})
	}))

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
			// The dashboard shows the effective display values: rename
			// override first, then registry, then agent-reported.
			cp.Info.Name = cp.Info.EffName(cp.Report)
			cp.Info.Role = cp.Info.EffDesc(cp.Report)
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
