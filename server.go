package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const maxAssetBytes = 32 << 20

// Server is the HTTP interface. The fetchImage and now fields are replaced in tests.
type Server struct {
	cfg        Config
	karakeep   *Karakeep
	fetchImage func(ctx context.Context, url string) *sourceImage
	now        func() time.Time
	log        *slog.Logger
	stats      stats
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, "application/json", []byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /opds", s.auth(s.opdsCatalog))
	mux.HandleFunc("GET /opds/recent", s.auth(s.opdsFeed(1, false)))
	mux.HandleFunc("GET /opds/search", s.auth(s.opdsFeed(1, true)))
	mux.HandleFunc("GET /opds2", s.auth(s.opds2Catalog))
	mux.HandleFunc("GET /opds2/recent", s.auth(s.opdsFeed(2, false)))
	mux.HandleFunc("GET /opds2/search", s.auth(s.opdsFeed(2, true)))
	mux.HandleFunc("GET /opds/bookmarks/{file}", s.auth(s.epub))
	// Without DASHBOARD_TOKEN the route does not exist (404). It uses its own bearer token
	// rather than the OPDS login, since a dashboard usually cannot send Basic credentials.
	if s.cfg.DashboardToken != "" {
		mux.HandleFunc("GET /api/dashboard", s.dashboard)
	}
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		detail(w, http.StatusNotFound, "Not Found")
	})
	return s.logRequests(mux)
}

// auth requires Basic authentication. The responses match FastAPI's HTTPBasic, except that a
// wrong password also gets WWW-Authenticate, so the reader prompts again.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		w.Header().Set("WWW-Authenticate", "Basic")
		if !ok {
			detail(w, http.StatusUnauthorized, "Not authenticated")
			return
		}
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.Username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(s.cfg.Password)) == 1
		if !userOK || !passOK {
			detail(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		w.Header().Del("WWW-Authenticate")
		next(w, r)
	}
}

// baseURL is SERVICE_BASE_URL, otherwise the scheme and host of the request.
func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.ServiceOrigin != "" {
		return s.cfg.ServiceOrigin
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	return scheme + "://" + r.Host
}

func (s *Server) opdsCatalog(w http.ResponseWriter, r *http.Request) {
	writeXML(w, renderOPDS1Catalog(s.baseURL(r), s.now()))
}

func (s *Server) opds2Catalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, typeOPDS2, renderOPDS2Catalog(s.baseURL(r)))
}

func (s *Server) opdsFeed(ver int, search bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		var (
			page  BookmarkPage
			err   error
			title = "Recent bookmarks"
			query *string
		)
		if search {
			q := r.URL.Query().Get("q")
			query = &q
			title = "Search: " + q
			if strings.TrimSpace(q) == "" {
				// Karakeep rejects an empty search (the original returned 502). The catalog's
				// search link points here with an empty q, so answer with an empty list.
				page = BookmarkPage{Items: []FeedItem{}}
			} else {
				page, err = s.karakeep.SearchBookmarks(r.Context(), q, cursor)
			}
		} else {
			page, err = s.karakeep.ListBookmarks(r.Context(), cursor)
		}
		if err != nil {
			s.badGateway(w, r, err)
			return
		}
		s.stats.feeds.add()
		self := r.URL.Path
		if ver == 1 {
			writeXML(w, renderOPDS1Feed(title, page, s.baseURL(r), self, query, s.now()))
		} else {
			writeJSON(w, http.StatusOK, typeOPDS2, renderOPDS2Feed(title, page, s.baseURL(r), self, query))
		}
	}
}

func (s *Server) epub(w http.ResponseWriter, r *http.Request) {
	id, ok := strings.CutSuffix(r.PathValue("file"), ".epub")
	if !ok || id == "" {
		detail(w, http.StatusNotFound, "Not Found")
		return
	}
	ctx := r.Context()
	item, err := s.karakeep.GetBookmark(ctx, id)
	if errors.Is(err, errNotFound) {
		item, err = nil, nil
	}
	if err != nil {
		s.badGateway(w, r, err)
		return
	}
	if item == nil {
		detail(w, http.StatusNotFound, "Bookmark not available as EPUB")
		return
	}

	articleHTML := item.HTMLContent
	if item.ContentAssetID != "" {
		data, mt, err := s.karakeep.GetAsset(ctx, item.ContentAssetID, maxAssetBytes)
		if err == nil && mt == "text/html" {
			articleHTML = strings.ToValidUTF8(string(data), "�")
		} else if err != nil && !errors.Is(err, errNotFound) {
			s.log.Warn("could not fetch article HTML", "bookmark", id, "err", err)
		}
	}
	var images []sourceImage
	if item.ImageAssetID != "" {
		data, mt, err := s.karakeep.GetAsset(ctx, item.ImageAssetID, maxAssetBytes)
		if err == nil && supportedEPUBImage(mt) {
			images = append(images, sourceImage{data: data, mediaType: mt, source: item.ImageURL})
		}
	}
	// The article's images are fetched concurrently but added in document order.
	urls := extractImageURLs(articleHTML)
	fetched := make([]*sourceImage, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Go(func() { fetched[i] = s.fetchImage(ctx, u) })
	}
	wg.Wait()
	for _, im := range fetched {
		if im != nil {
			images = append(images, *im)
		}
	}

	book, err := renderEPUB(*item, articleHTML, images, version, s.now())
	if err != nil {
		s.log.Error("could not build EPUB", "bookmark", id, "err", err)
		detail(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	s.stats.epubs.add()
	w.Header().Set("Content-Type", typeEPUB)
	w.Header().Set("Content-Disposition", `attachment; filename="`+headerSafe(id)+`.epub"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(book)
}

func (s *Server) badGateway(w http.ResponseWriter, r *http.Request, err error) {
	s.stats.failures.add()
	s.log.Warn("Karakeep request failed", "path", r.URL.Path, "err", err)
	detail(w, http.StatusBadGateway, "Karakeep API request failed")
}

// headerSafe replaces characters that are not allowed in a Content-Disposition filename.
func headerSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' || r == 0x7f || r >= utf8.RuneSelf {
			return '_'
		}
		return r
	}, s)
}

func writeXML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

func writeJSON(w http.ResponseWriter, status int, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// detail responds like FastAPI's HTTPException: {"detail": "..."}.
func detail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, "application/json", compactJSON(map[string]string{"detail": msg}))
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// logRequests logs method, path, status and duration. The query string (search terms) and
// credentials are not logged.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/dashboard" {
			return
		}
		s.log.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"ms", time.Since(start).Milliseconds())
	})
}
