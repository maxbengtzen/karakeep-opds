// karakeep-opds serves Karakeep bookmarks as OPDS catalogs (1.2 and 2) and builds an EPUB
// per bookmark, so they can be read on an e-reader.
//
// Go port of https://github.com/yazdipour/karakeep-opds (GPL-3.0-or-later).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var version = "dev"

// Config is read from environment variables. The names are the same as in the original.
type Config struct {
	KarakeepOrigin string // KARAKEEP_BASE_URL + KARAKEEP_API_PATH
	Token          string
	Username       string
	Password       string
	PageSize       int
	ServiceOrigin  string
	ListenAddr     string
	DashboardToken string // DASHBOARD_TOKEN, for /api/dashboard
}

func loadConfig(lookupEnv func(string) (string, bool)) (Config, error) {
	getenv := func(k string) string { v, _ := lookupEnv(k); return v }
	var errs []string
	need := func(key string) string {
		v := getenv(key)
		if v == "" {
			errs = append(errs, key+" is missing")
		}
		return v
	}
	cfg := Config{
		Token:          need("KARAKEEP_API_TOKEN"),
		Username:       need("OPDS_USERNAME"),
		Password:       need("OPDS_PASSWORD"),
		PageSize:       50,
		ListenAddr:     getenv("LISTEN_ADDR"),
		DashboardToken: strings.TrimSpace(getenv("DASHBOARD_TOKEN")),
	}
	if cfg.DashboardToken != "" && len(cfg.DashboardToken) < 16 {
		errs = append(errs, "DASHBOARD_TOKEN must be at least 16 characters (e.g. openssl rand -hex 32)")
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":8000"
	}

	base, err := httpOrigin(need("KARAKEEP_BASE_URL"))
	if err != nil {
		errs = append(errs, "KARAKEEP_BASE_URL: "+err.Error())
	}
	apiPath := "/api/v1"
	if v, ok := lookupEnv("KARAKEEP_API_PATH"); ok { // empty means no path
		apiPath = ""
		if t := strings.Trim(strings.TrimSpace(v), "/"); t != "" {
			apiPath = "/" + t
		}
	}
	cfg.KarakeepOrigin = base + apiPath

	if v := strings.TrimSpace(getenv("OPDS_PAGE_SIZE")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			errs = append(errs, "OPDS_PAGE_SIZE must be an integer from 1 to 200")
		}
		cfg.PageSize = n
	}
	if v := strings.TrimSpace(getenv("SERVICE_BASE_URL")); v != "" {
		cfg.ServiceOrigin, err = httpOrigin(v)
		if err != nil {
			errs = append(errs, "SERVICE_BASE_URL: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return cfg, errors.New(strings.Join(errs, "; "))
	}
	return cfg, nil
}

// httpOrigin validates an http(s) URL and strips trailing slashes. A URL without a scheme
// (like 127.0.0.1:3000 in the original's example) gets http://.
func httpOrigin(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	u, err := url.Parse(v)
	if err != nil {
		return "", err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("%q is not an http(s) URL", v)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "healthcheck":
			os.Exit(healthcheck())
		case "version", "-version", "--version":
			fmt.Println(version)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown command %q (known: healthcheck, version)\n", os.Args[1])
			os.Exit(2)
		}
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg, err := loadConfig(os.LookupEnv)
	if err != nil {
		logger.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	imageClient := newImageClient(15 * time.Second)
	srv := &Server{
		cfg: cfg,
		karakeep: &Karakeep{
			origin:   cfg.KarakeepOrigin,
			token:    cfg.Token,
			pageSize: cfg.PageSize,
			http:     &http.Client{Timeout: 20 * time.Second},
			now:      time.Now,
		},
		fetchImage: func(ctx context.Context, u string) *sourceImage {
			return fetchRemoteImage(ctx, imageClient, u)
		},
		now: time.Now,
		log: logger,
	}
	srv.stats.started = time.Now()
	httpSrv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cfg.DashboardToken != "" {
		go srv.watchKarakeep(ctx)
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdown)
	}()
	logger.Info("karakeep-opds starting", "version", version, "addr", cfg.ListenAddr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

// healthcheck is used by Docker (the scratch image has neither curl nor wget).
func healthcheck() int {
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8000"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "status", resp.Status)
		return 1
	}
	return 0
}
