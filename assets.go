package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// assetCache holds the last successful fetch from the configured external
// asset-inventory source (e.g. an internal cloud account inventory API that
// returns a JSON array of assets). The source is polled on a fixed interval;
// a failed fetch never wipes the last good data and never stops the server.
type assetCache struct {
	mu      sync.Mutex
	items   []map[string]any
	fetched int64  // unix seconds of last successful fetch
	err     string // last fetch error ("" when ok)
	cfg     ServerConfig
	client  *http.Client
}

func newAssetCache(cfg ServerConfig) *assetCache {
	return &assetCache{cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}}
}

func (a *assetCache) setErr(msg string) {
	a.mu.Lock()
	a.err = msg
	a.mu.Unlock()
}

func (a *assetCache) refresh() {
	if a.cfg.AssetsURL == "" {
		return
	}
	req, err := http.NewRequest("GET", a.cfg.AssetsURL, nil)
	if err != nil {
		a.setErr(err.Error())
		return
	}
	for k, v := range a.cfg.AssetsHeaders {
		req.Header.Set(k, v)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		a.setErr(err.Error())
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		a.setErr(fmt.Sprintf("source returned HTTP %d", resp.StatusCode))
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		a.setErr(err.Error())
		return
	}
	var items []map[string]any
	// Accept either a bare JSON array or an object with an "items" array.
	if err := json.Unmarshal(body, &items); err != nil {
		var wrap struct {
			Items []map[string]any `json:"items"`
		}
		if err2 := json.Unmarshal(body, &wrap); err2 != nil || wrap.Items == nil {
			a.setErr(`source is not a JSON array or {"items":[...]}`)
			return
		}
		items = wrap.Items
	}
	a.mu.Lock()
	a.items = items
	a.fetched = time.Now().Unix()
	a.err = ""
	a.mu.Unlock()
}

// snapshot returns the cache contents in the shape served at /api/assets.
func (a *assetCache) snapshot() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.items == nil {
		a.items = []map[string]any{}
	}
	out := map[string]any{
		"ok":         a.err == "",
		"count":      len(a.items),
		"items":      a.items,
		"fetched_at": a.fetched,
	}
	if a.cfg.AssetsURL != "" {
		out["source"] = a.cfg.AssetsURL
	}
	if a.err != "" {
		out["error"] = a.err
	}
	return out
}

// startAssetRefresher launches the background refresh loop. It runs for the
// lifetime of the process; a source failure never stops the server.
func startAssetRefresher(cfg ServerConfig) *assetCache {
	c := newAssetCache(cfg)
	if cfg.AssetsURL == "" {
		return c
	}
	go func() {
		c.refresh()
		interval := time.Duration(cfg.AssetsRefresh) * time.Second
		if interval < 30*time.Second {
			interval = 5 * time.Minute
		}
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			c.refresh()
		}
	}()
	return c
}
