package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestExcerpt(t *testing.T) {
	for _, c := range []struct {
		in    string
		limit int
		want  string
	}{
		{"", 10, ""},
		{"  a \n\t b  ", 10, "a b"},
		{"åäöåäö", 6, "åäöåäö"},
		{"åäöåäöx", 6, "åäöåä..."},
		{"abc   defgh", 6, "abc d..."},
		{"abcd efgh", 6, "abcd..."}, // rstrip before "..."
		{"a b c", 10, "a b c"},
		{"a\x1cb", 10, "a b"}, // Python counts 0x1c as whitespace
	} {
		if got := excerpt(c.in, c.limit); got != c.want {
			t.Errorf("excerpt(%q, %d) = %q, want %q", c.in, c.limit, got, c.want)
		}
	}
}

func TestHTMLToText(t *testing.T) {
	for in, want := range map[string]string{
		"":                                       "",
		"   ":                                    "",
		"<p>a &amp; b</p><IMG\nsrc=x>c":          "a & b c",
		"<p>x</p><img src='data:image/png;b'>y":  "x y",
		"text data:image/png;base64,AAA) end":    "text ) end",
		"![alt](data:image/png;base64,AAA) left": "left",
		"&eacute;&nbsp;&#x41;":                   "é A",
	} {
		if got := htmlToText(in); got != want {
			t.Errorf("htmlToText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseTimeAndISO(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for in, want := range map[any]string{
		"2026-11-12T05:57:47.000Z":        "2026-11-12T05:57:47Z",
		"2026-11-12T05:57:47.123Z":        "2026-11-12T05:57:47.123000Z",
		"2026-11-12T05:57:47.1234567Z":    "2026-11-12T05:57:47.123456Z",
		"2026-11-09T10:15:30+02:00":       "2026-11-09T08:15:30Z",
		"2026-11-07T11:00:00":             "2026-11-07T11:00:00Z",
		"2026-11-07 11:00:00":             "2026-11-07T11:00:00Z",
		"2026-11-07":                      "2026-11-07T00:00:00Z",
		"  2026-11-07T11:00:00.5+00:00  ": "2026-11-07T11:00:00.500000Z",
		"not a date":                      "2026-01-01T00:00:00Z",
		nil:                               "2026-01-01T00:00:00Z",
	} {
		if got := isoTime(parseTime(in, now)); got != want {
			t.Errorf("parseTime(%v) = %s, want %s", in, got, want)
		}
	}
}

func TestBestSrcsetURL(t *testing.T) {
	for in, want := range map[string]string{
		"":                          "",
		"a.jpg":                     "a.jpg",
		"a.jpg 320w, b.jpg 1024w":   "b.jpg",
		"b.jpg 1024w, a.jpg 320w":   "b.jpg",
		"a.jpg 2x, b.jpg":           "b.jpg", // no width: 0, ties decided by the URL
		" c.jpg 100w , d.jpg 100w ": "d.jpg",
	} {
		if got := bestSrcsetURL(in); got != want {
			t.Errorf("bestSrcsetURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestExtractImageURLs(t *testing.T) {
	h := `<img src="https://a/1.png"><img src="/rel.png"><img src="https://a/1.png">
<noscript><img src="https://a/ns.png"></noscript><img srcset="https://a/s.gif 10w, https://a/l.gif 99w">
<img src="http://a/3.jpg"><img src="https://a/4.jpg"><img src="https://a/5.jpg">`
	got := extractImageURLs(h)
	want := []string{"https://a/1.png", "https://a/ns.png", "https://a/l.gif", "http://a/3.jpg", "https://a/4.jpg"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestSanitizeArticle(t *testing.T) {
	img := &epubImage{path: "images/image-1.png"}
	got, used := sanitizeArticle(`<html><head><title>Do not leak</title></head><body>
<h1> The Title </h1><p>the title</p>
<div>Loose <span>text</span></div>
<p><a href="javascript:alert(1)">dangerous</a> <a href="mailto:a@b">mail</a> <a href="/rel">relative</a></p>
<noscript><img src="https://a/x.png" alt="ns"></noscript>
<p onclick="x" style="color:red">attributes</p><form><input value="gone"></form>
<p></p><p>   </p><strong></strong></body></html>`,
		map[string]*epubImage{"https://a/x.png": img}, "The Title")
	want := `<p>Loose text</p>
<p>dangerous <a href="mailto:a@b">mail</a> relative</p>
<p><img src="images/image-1.png" alt="ns"/></p>
<p>attributes</p>`
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if !used["images/image-1.png"] {
		t.Error("the image was not marked as used")
	}
}

func TestEPUBImagesDedupe(t *testing.T) {
	png := []byte("\x89PNG-same")
	imgs := epubImages([]sourceImage{
		{data: png, mediaType: "image/png", source: "https://a/1.png"},
		{data: []byte("webp"), mediaType: "image/webp", source: "https://a/2.webp"},
		{data: png, mediaType: "image/png", source: "https://a/copy.png"},
		{data: []byte("jpeg"), mediaType: "image/jpeg"},
	})
	if len(imgs) != 2 || imgs[0].path != "images/image-1.png" || imgs[1].path != "images/image-2.jpg" {
		t.Fatalf("got %+v", imgs)
	}
	if strings.Join(imgs[0].sources, " ") != "https://a/1.png https://a/copy.png" {
		t.Errorf("sources %v", imgs[0].sources)
	}
}

func TestUUID5(t *testing.T) {
	// The value comes from the Python original (golden epub-link-full).
	if got := uuid5URL("karakeep:bookmark:bm_link_full"); got != "461a3daa-1cda-50ff-884f-5fc47ac29982" {
		t.Errorf("got %s", got)
	}
}

func TestPublicIP(t *testing.T) {
	for s, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true, "93.184.216.34": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "224.0.0.1": false,
		"::1": false, "fe80::1": false, "fd00::1": false, "::ffff:127.0.0.1": false,
		"::ffff:192.168.1.1": false, "64:ff9b::a00:1": false, "198.18.0.1": false, "255.255.255.255": false,
	} {
		if got := publicIP(netip.MustParseAddr(s)); got != want {
			t.Errorf("publicIP(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestFetchRemoteImageBlocksPrivate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	defer srv.Close()
	// The server listens on 127.0.0.1, so the protected client must refuse to connect.
	if im := fetchRemoteImage(context.Background(), newImageClient(5*time.Second), srv.URL+"/a.png"); im != nil {
		t.Fatal("fetched from an internal address")
	}
	for _, u := range []string{"file:///etc/passwd", "ftp://x/a.png", "https:///a.png", "::"} {
		if fetchRemoteImage(context.Background(), srv.Client(), u) != nil {
			t.Errorf("%s was accepted", u)
		}
	}
}

func TestFetchRemoteImageRules(t *testing.T) {
	big := strings.Repeat("x", maxRemoteImageBytes+1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok.png":
			w.Header().Set("Content-Type", "image/png; charset=binary")
			_, _ = w.Write([]byte("png"))
		case "/max.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte(big[:maxRemoteImageBytes]))
		case "/big.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte(big))
		case "/a.webp":
			w.Header().Set("Content-Type", "image/webp")
			_, _ = w.Write([]byte("webp"))
		case "/r1":
			http.Redirect(w, r, "/r2", http.StatusFound)
		case "/r2":
			http.Redirect(w, r, "/r3", http.StatusMovedPermanently)
		case "/r3":
			http.Redirect(w, r, "/ok.png", http.StatusTemporaryRedirect)
		case "/r0":
			http.Redirect(w, r, "/r1", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(p string) *sourceImage { return fetchRemoteImage(context.Background(), client, srv.URL+p) }

	if im := get("/ok.png"); im == nil || im.mediaType != "image/png" || string(im.data) != "png" || im.source != srv.URL+"/ok.png" {
		t.Errorf("ok.png: %+v", im)
	}
	if get("/max.jpg") == nil {
		t.Error("exactly 2 MB must be accepted")
	}
	if get("/big.jpg") != nil {
		t.Error("over 2 MB must be rejected")
	}
	if get("/a.webp") != nil {
		t.Error("webp must be rejected")
	}
	if get("/missing.png") != nil {
		t.Error("404 must give nil")
	}
	if im := get("/r1"); im == nil || im.source != srv.URL+"/r1" {
		t.Errorf("three redirects must be followed: %+v", im)
	}
	if get("/r0") != nil {
		t.Error("four redirects must be rejected")
	}
}

func TestLoadConfig(t *testing.T) {
	base := map[string]string{
		"KARAKEEP_BASE_URL": "http://karakeep:3000/", "KARAKEEP_API_TOKEN": "t",
		"OPDS_USERNAME": "u", "OPDS_PASSWORD": "p",
	}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}
	cfg, err := loadConfig(mapEnv(base))
	if err != nil || cfg.KarakeepOrigin != "http://karakeep:3000/api/v1" || cfg.PageSize != 50 || cfg.ListenAddr != ":8000" || cfg.ServiceOrigin != "" {
		t.Errorf("defaults: %+v %v", cfg, err)
	}
	cfg, _ = loadConfig(mapEnv(with("KARAKEEP_API_PATH", "", "KARAKEEP_BASE_URL", "127.0.0.1:3000")))
	if cfg.KarakeepOrigin != "http://127.0.0.1:3000" {
		t.Errorf("empty API path: %q", cfg.KarakeepOrigin)
	}
	cfg, _ = loadConfig(mapEnv(with("KARAKEEP_API_PATH", "/x/y/", "SERVICE_BASE_URL", "https://opds.example.com/", "OPDS_PAGE_SIZE", "200")))
	if cfg.KarakeepOrigin != "http://karakeep:3000/x/y" || cfg.ServiceOrigin != "https://opds.example.com" || cfg.PageSize != 200 {
		t.Errorf("custom values: %+v", cfg)
	}
	for _, bad := range []map[string]string{
		with("OPDS_PAGE_SIZE", "0"), with("OPDS_PAGE_SIZE", "201"), with("OPDS_PASSWORD", ""),
		with("KARAKEEP_BASE_URL", "ftp://x"), with("SERVICE_BASE_URL", "not a url://"),
		with("DASHBOARD_TOKEN", "too-short"),
	} {
		if _, err := loadConfig(mapEnv(bad)); err == nil {
			t.Errorf("should fail: %v", bad)
		}
	}
}

func TestServiceBaseURLAndForwardedProto(t *testing.T) {
	env := newFakeEnv(t)
	do := func(setup func(*http.Request)) string {
		req := httptest.NewRequest(http.MethodGet, "http://opds.local/opds2", nil)
		req.SetBasicAuth("reader", "secret")
		setup(req)
		rec := httptest.NewRecorder()
		env.srv.Handler().ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if b := do(func(r *http.Request) { r.Header.Set("X-Forwarded-Proto", "https") }); !strings.Contains(b, `"href":"https://opds.local/opds2"`) {
		t.Errorf("X-Forwarded-Proto: %s", b)
	}
	env.srv.cfg.ServiceOrigin = "https://karakeep-opds.example.org"
	if b := do(func(*http.Request) {}); !strings.Contains(b, `"href":"https://karakeep-opds.example.org/opds2/recent"`) {
		t.Errorf("SERVICE_BASE_URL: %s", b)
	}
}

func TestNotFoundAndMethods(t *testing.T) {
	env := newFakeEnv(t)
	h := env.srv.Handler()
	for _, c := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/does-not-exist", 404},
		{"GET", "/opds/bookmarks/bm_text.pdf", 404},
		{"POST", "/opds", 405},
		{"HEAD", "/healthz", 200},
	} {
		req := httptest.NewRequest(c.method, "http://testserver"+c.path, nil)
		req.SetBasicAuth("reader", "secret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.status {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.status)
		}
	}
}
