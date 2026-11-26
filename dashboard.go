package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// GET /api/dashboard returns a small card for a homepage dashboard: a few aggregates as JSON.
// The counters live in memory and start over on restart.

// metric is one value on the card: k is a short lower-case label, v a number or a ready-made string.
type metric struct {
	K string `json:"k"`
	V any    `json:"v"`
}

type dashboardResponse struct {
	Metrics []metric `json:"metrics"`
	Foot    string   `json:"foot,omitempty"`
	Status  string   `json:"status,omitempty"`
	Message string   `json:"message,omitempty"`
}

// stats holds the counters and Karakeep's status. The zero value is ready to use.
type stats struct {
	epubs, feeds, failures window
	karakeep               probe
	started                time.Time
}

// window counts events in hourly buckets over the last 24 hours. The zero value is ready to use.
type window struct {
	now func() time.Time // time.Now if nil

	mu      sync.Mutex
	buckets [24]struct{ hour, n int64 }
}

func (w *window) hour() int64 {
	now := time.Now
	if w.now != nil {
		now = w.now
	}
	return now().Unix() / 3600
}

func (w *window) add() {
	h := w.hour()
	w.mu.Lock()
	b := &w.buckets[h%24]
	if b.hour != h {
		b.hour, b.n = h, 0
	}
	b.n++
	w.mu.Unlock()
}

// day is the number of events in the last 24 hours.
func (w *window) day() int64 {
	h := w.hour()
	w.mu.Lock()
	defer w.mu.Unlock()
	var n int64
	for _, b := range w.buckets {
		if b.hour > h-24 && b.hour <= h {
			n += b.n
		}
	}
	return n
}

// probe is Karakeep's answer to the latest check: the HTTP status, or 0 if Karakeep was unreachable.
type probe struct {
	mu      sync.Mutex
	checked bool
	status  int
}

func (p *probe) set(status int) {
	p.mu.Lock()
	p.checked, p.status = true, status
	p.mu.Unlock()
}

func (p *probe) last() (checked bool, status int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.checked, p.status
}

// checkKarakeep asks for one bookmark, which tests both the URL and the token.
func (s *Server) checkKarakeep(ctx context.Context, timeout time.Duration) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	status := 0
	resp, err := s.karakeep.do(cctx, "/bookmarks", url.Values{"limit": {"1"}})
	if err == nil {
		status = resp.StatusCode
		resp.Body.Close()
	}
	if ctx.Err() == nil {
		s.stats.karakeep.set(status)
	}
}

// watchKarakeep checks Karakeep now and then every minute, so the card can answer immediately.
func (s *Server) watchKarakeep(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.checkKarakeep(ctx, 10*time.Second)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// dashboard requires DASHBOARD_TOKEN as a bearer token (compared in constant time).
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	h := r.Header.Get("Authorization")
	got := ""
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		got = strings.TrimSpace(h[7:])
	}
	gotSum, wantSum := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(s.cfg.DashboardToken))
	if s.cfg.DashboardToken == "" || subtle.ConstantTimeCompare(gotSum[:], wantSum[:]) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="dashboard"`)
		detail(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	resp := dashboardResponse{
		Metrics: []metric{
			{K: "epub 24 h", V: s.stats.epubs.day()},
			{K: "feeds 24 h", V: s.stats.feeds.day()},
			{K: "errors 24 h", V: s.stats.failures.day()},
		},
		Foot: since(s.stats.started, s.now()),
	}
	switch checked, status := s.stats.karakeep.last(); {
	case !checked: // not checked yet: no verdict
	case status >= 200 && status <= 299:
		resp.Status = "ok"
	case status == 0:
		resp.Status, resp.Message = "down", "Karakeep is not responding"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		resp.Status, resp.Message = "warn", "Karakeep rejects the API token"
	case status >= 500:
		resp.Status, resp.Message = "down", fmt.Sprintf("Karakeep returns an error (HTTP %d)", status)
	default:
		resp.Status, resp.Message = "warn", fmt.Sprintf("Karakeep returns HTTP %d", status)
	}
	b, _ := json.Marshal(resp)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, "application/json", b)
}

// since is a footer saying that the counters started at a restart less than 24 hours ago,
// otherwise "" (the dashboard then shows its own footer).
func since(started, now time.Time) string {
	d := now.Sub(started)
	if started.IsZero() || d >= 24*time.Hour {
		return ""
	}
	if d < time.Hour {
		return fmt.Sprintf("counted since start %d min ago", int(d.Minutes()))
	}
	return fmt.Sprintf("counted since start %d h ago", int(d.Hours()))
}
