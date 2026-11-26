package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// The golden tests make the same requests that scripts/golden/record.py made against the
// Python original, with the same fake Karakeep (testdata/fixtures/routes.json), and compare the
// responses with testdata/golden/. Intentional differences are listed in knownDiffs and in
// testdata/README.md.
//
// The EPUB chapter (content.xhtml) is not compared with the original, since the original went
// through Markdown. It is compared instead with a hand-reviewed snapshot in
// testdata/golden-go/. Update it with: go test -run Golden -update

var update = flag.Bool("update", false, "rewrite testdata/golden-go")

var fixedNow = time.Date(2026, 1, 2, 3, 4, 5, 678901000, time.UTC)

type route struct {
	Path   string            `json:"path"`
	Query  map[string]string `json:"query"`
	File   string            `json:"file"`
	Body   string            `json:"body"`
	Status int               `json:"status"`
	Type   string            `json:"type"`
}

type routes struct {
	Karakeep []route `json:"karakeep"`
	Remote   map[string]struct {
		File string `json:"file"`
		Type string `json:"type"`
	} `json:"remote"`
}

type goldenCase struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Auth string `json:"auth"`
	EPUB bool   `json:"epub"`
}

type goldenMeta struct {
	Status             int              `json:"status"`
	ContentType        *string          `json:"content_type"`
	ContentDisposition *string          `json:"content_disposition"`
	WWWAuthenticate    *string          `json:"www_authenticate"`
	Upstream           []map[string]any `json:"upstream"`
}

// knownDiffs are intentional differences from the original: field name → value in the Go version.
var knownDiffs = map[string]map[string]any{
	// A wrong password asks the reader to log in again.
	"opds-badauth": {"www_authenticate": "Basic"},
	// A bookmark that does not exist gives 404, not 502.
	"epub-missing": {"status": 404, "body": `{"detail":"Bookmark not available as EPUB"}`},
	// An empty search does not ask Karakeep (which answers 400 to an empty q) but gives an empty list.
	"opds-search-empty":  {"upstream": []map[string]any{}},
	"opds2-search-empty": {"upstream": []map[string]any{}},
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

// fakeEnv starts a fake Karakeep and a Server that talks to it.
type fakeEnv struct {
	srv *Server
	mu  sync.Mutex
	log []map[string]any
}

func newFakeEnv(t *testing.T) *fakeEnv {
	t.Helper()
	var rt routes
	readJSON(t, "testdata/fixtures/routes.json", &rt)
	dir := "testdata/fixtures/karakeep/"
	env := &fakeEnv{}
	kk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := map[string]string{}
		for k, vs := range r.URL.Query() {
			q[k] = vs[len(vs)-1]
		}
		env.record(map[string]any{"path": r.URL.Path, "query": q, "authorization": r.Header.Get("Authorization")})
		for _, rr := range rt.Karakeep {
			if rr.Path != r.URL.Path || !reflect.DeepEqual(rr.Query, q) {
				continue
			}
			body := []byte(rr.Body)
			if rr.File != "" {
				var err error
				if body, err = os.ReadFile(dir + rr.File); err != nil {
					t.Error(err)
				}
			}
			typ := rr.Type
			if typ == "" {
				typ = "application/json"
			}
			w.Header().Set("Content-Type", typ)
			if rr.Status != 0 {
				w.WriteHeader(rr.Status)
			}
			_, _ = w.Write(body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"code":"NOT_FOUND"}`)
	}))
	t.Cleanup(kk.Close)

	cfg, err := loadConfig(mapEnv(map[string]string{
		"KARAKEEP_BASE_URL": kk.URL, "KARAKEEP_API_TOKEN": "test-token",
		"OPDS_USERNAME": "reader", "OPDS_PASSWORD": "secret",
	}))
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return fixedNow }
	env.srv = &Server{
		cfg: cfg,
		karakeep: &Karakeep{
			origin: cfg.KarakeepOrigin, token: cfg.Token, pageSize: cfg.PageSize,
			http: kk.Client(), now: now,
		},
		fetchImage: func(_ context.Context, u string) *sourceImage {
			env.record(map[string]any{"remote": u})
			e, ok := rt.Remote[u]
			if !ok {
				return nil
			}
			data, err := os.ReadFile(dir + e.File)
			if err != nil {
				t.Error(err)
			}
			return &sourceImage{data: data, mediaType: e.Type, source: u}
		},
		now: now,
		log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return env
}

func mapEnv(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}
}

func (e *fakeEnv) record(m map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.log = append(e.log, m)
}

// takeLog returns the requests: Karakeep in order, image fetches sorted (they run concurrently).
func (e *fakeEnv) takeLog() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.log
	e.log = nil
	return normalizeUpstream(out)
}

func normalizeUpstream(in []map[string]any) []map[string]any {
	var kk, remote []map[string]any
	for _, m := range in {
		if _, ok := m["remote"]; ok {
			remote = append(remote, m)
		} else {
			kk = append(kk, m)
		}
	}
	sort.Slice(remote, func(i, j int) bool {
		return fmt.Sprint(remote[i]["remote"]) < fmt.Sprint(remote[j]["remote"])
	})
	// JSON round trip so the types are the same as in the golden files.
	b, _ := json.Marshal(append(kk, remote...))
	var out []map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func strp(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func TestGolden(t *testing.T) {
	env := newFakeEnv(t)
	h := env.srv.Handler()
	var cases []goldenCase
	readJSON(t, "testdata/fixtures/cases.json", &cases)
	auth := map[string]string{"": "reader:secret", "ok": "reader:secret", "bad": "reader:wrong"}

	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://testserver"+c.Path, nil)
			if cred, ok := auth[c.Auth]; ok {
				u, p, _ := strings.Cut(cred, ":")
				req.SetBasicAuth(u, p)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			var want goldenMeta
			readJSON(t, "testdata/golden/"+c.Name+".meta.json", &want)
			want.Upstream = normalizeUpstream(want.Upstream)
			wantBody, _ := os.ReadFile("testdata/golden/" + c.Name + ".body")
			for k, v := range knownDiffs[c.Name] {
				switch k {
				case "status":
					want.Status = v.(int)
				case "www_authenticate":
					want.WWWAuthenticate = strp(v.(string))
				case "body":
					wantBody = []byte(v.(string))
				case "upstream":
					want.Upstream = v.([]map[string]any)
				}
			}

			got := goldenMeta{
				Status:             rec.Code,
				ContentType:        strp(rec.Header().Get("Content-Type")),
				ContentDisposition: strp(rec.Header().Get("Content-Disposition")),
				WWWAuthenticate:    strp(rec.Header().Get("WWW-Authenticate")),
				Upstream:           env.takeLog(),
			}
			if got.Upstream == nil {
				got.Upstream = []map[string]any{}
			}
			if want.Upstream == nil {
				want.Upstream = []map[string]any{}
			}
			gm, _ := json.MarshalIndent(got, "", "  ")
			wm, _ := json.MarshalIndent(want, "", "  ")
			if !bytes.Equal(gm, wm) {
				t.Errorf("meta differs\n--- got\n%s\n--- want\n%s", gm, wm)
			}
			if c.EPUB && rec.Code == http.StatusOK {
				checkEPUB(t, c.Name, rec.Body.Bytes())
				return
			}
			if !bytes.Equal(rec.Body.Bytes(), wantBody) {
				t.Errorf("body differs\n--- got\n%s\n--- want\n%s", rec.Body.Bytes(), wantBody)
			}
		})
	}
}

var opfVolatile = regexp.MustCompile(`(?m)^    <meta (property="dcterms:modified">.*</meta>|name="generator" content=".*"/>)\n`)

func checkEPUB(t *testing.T, name string, data []byte) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var list strings.Builder
	files := map[string][]byte{}
	for _, f := range zr.File {
		method := "deflated"
		if f.Method == zip.Store {
			method = "stored"
		}
		fmt.Fprintf(&list, "%s %s\n", f.Name, method)
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	wantList, _ := os.ReadFile("testdata/golden/" + name + ".epub.list")
	if list.String() != string(wantList) {
		t.Errorf("files in the EPUB differ\n--- got\n%s--- want\n%s", list.String(), wantList)
	}

	dir := "testdata/golden/" + name + ".epub.d/"
	for fname, got := range files {
		want, err := os.ReadFile(dir + fname)
		if err != nil {
			t.Errorf("%s: %v", fname, err)
			continue
		}
		switch fname {
		case "EPUB/content.xhtml":
			checkChapter(t, name, got, files)
			continue
		case "EPUB/content.opf":
			got = opfVolatile.ReplaceAll(got, nil)
			want = opfVolatile.ReplaceAll(want, nil)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs\n--- got\n%s\n--- want\n%s", fname, got, want)
		}
	}
	if !bytes.Contains(files["EPUB/content.opf"], []byte(`<meta property="dcterms:modified">2026-01-02T03:04:05Z</meta>`)) {
		t.Error("content.opf is missing dcterms:modified")
	}
}

var imgSrcRe = regexp.MustCompile(`<img src="([^"]+)"`)

// checkChapter: the XHTML must be well-formed, every image must exist in the EPUB and the text
// must match the snapshot.
func checkChapter(t *testing.T, name string, got []byte, files map[string][]byte) {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(got))
	dec.Strict = true
	dec.Entity = map[string]string{}
	for {
		_, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("content.xhtml is not well-formed XML: %v\n%s", err, got)
		}
	}
	for _, m := range imgSrcRe.FindAllSubmatch(got, -1) {
		if _, ok := files["EPUB/"+string(m[1])]; !ok {
			t.Errorf("image %s is not in the EPUB", m[1])
		}
	}
	for _, bad := range []string{"<script", "<iframe", "<style", "data:", "class=", "svg-text"} {
		if bytes.Contains(got, []byte(bad)) {
			t.Errorf("content.xhtml contains %q", bad)
		}
	}
	snap := filepath.Join("testdata/golden-go", name+".content.xhtml")
	if *update {
		if err := os.MkdirAll(filepath.Dir(snap), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(snap, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(snap)
	if err != nil {
		t.Fatalf("%v (run go test -run Golden -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("content.xhtml differs from %s\n--- got\n%s\n--- want\n%s", snap, got, want)
	}
}
