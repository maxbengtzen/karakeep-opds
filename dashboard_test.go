package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWindow(t *testing.T) {
	now := time.Date(2026, 11, 16, 10, 30, 0, 0, time.UTC)
	w := &window{now: func() time.Time { return now }}
	w.add()
	w.add()
	now = now.Add(23 * time.Hour)
	w.add()
	if n := w.day(); n != 3 {
		t.Fatalf("day: %d", n)
	}
	now = now.Add(time.Hour) // the first hour has left the window
	if n := w.day(); n != 1 {
		t.Fatalf("after 24 h: %d", n)
	}
	now = now.Add(48 * time.Hour) // a reused bucket starts over
	w.add()
	if n := w.day(); n != 1 {
		t.Fatalf("after 72 h: %d", n)
	}
}

func TestDashboardCounts(t *testing.T) {
	env := newFakeEnv(t)
	get := func(h http.Handler, path, bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://testserver"+path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		} else {
			req.SetBasicAuth("reader", "secret")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	// Without DASHBOARD_TOKEN the route does not exist, not even with the OPDS login.
	if rec := get(env.srv.Handler(), "/api/dashboard", ""); rec.Code != 404 {
		t.Fatalf("without token: %d", rec.Code)
	}
	env.srv.cfg.DashboardToken = "0123456789abcdef"
	h := env.srv.Handler()
	for _, bearer := range []string{"", "wrong-token-0123456789"} {
		if rec := get(h, "/api/dashboard", bearer); rec.Code != 401 {
			t.Errorf("token %q: %d", bearer, rec.Code)
		}
	}
	for _, p := range []string{"/opds", "/opds/recent", "/opds2/search?q=%C3%A5+%26+b", "/opds/recent?cursor=boom",
		"/opds/bookmarks/bm_text.epub", "/opds/bookmarks/bm_missing.epub"} {
		get(h, p, "")
	}
	rec := get(h, "/api/dashboard", "0123456789abcdef")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	want := `{"metrics":[{"k":"epub 24 h","v":1},{"k":"feeds 24 h","v":2},{"k":"errors 24 h","v":1}]}`
	if rec.Body.String() != want {
		t.Fatalf("\n%s\nwant\n%s", rec.Body, want)
	}
}

func TestDashboardKarakeepStatus(t *testing.T) {
	status := 200
	kk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/bookmarks" || r.URL.Query().Get("limit") != "1" || r.Header.Get("Authorization") != "Bearer kk" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.WriteHeader(status)
	}))
	now := time.Now()
	s := &Server{
		cfg:      Config{DashboardToken: "0123456789abcdef"},
		karakeep: &Karakeep{origin: kk.URL + "/api/v1", token: "kk", http: kk.Client()},
		now:      func() time.Time { return now },
		log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	s.stats.started = now.Add(-3 * time.Hour)
	h := s.Handler()
	check := func() dashboardResponse {
		s.checkKarakeep(context.Background(), time.Second)
		req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
		req.Header.Set("Authorization", "Bearer 0123456789abcdef")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var r dashboardResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	if r := check(); r.Status != "ok" || r.Message != "" || r.Foot != "counted since start 3 h ago" {
		t.Errorf("ok: %+v", r)
	}
	status = 401
	if r := check(); r.Status != "warn" || r.Message != "Karakeep rejects the API token" {
		t.Errorf("401: %+v", r)
	}
	status = 503
	if r := check(); r.Status != "down" || r.Message != "Karakeep returns an error (HTTP 503)" {
		t.Errorf("503: %+v", r)
	}
	kk.Close()
	if r := check(); r.Status != "down" || r.Message != "Karakeep is not responding" {
		t.Errorf("down: %+v", r)
	}
}
