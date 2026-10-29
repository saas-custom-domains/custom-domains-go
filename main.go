// Snip is an example link shortener where each brand can serve its
// short links on its own domain (go.acme.com/launch), with SaaS Custom
// Domains in front for routing and HTTPS. It goes with the guide
// https://saascustomdomains.com/blog/posts/custom-domains-go
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	Port      string
	DataDir   string
	AppHosts  []string
	AppDomain string

	SCDAPIURL      string
	SCDAPIToken    string
	SCDAccount     string
	SCDUpstream    string
	ProxyAuthToken string // the upstream's auth token
	WebhookToken   string
}

func configFromEnv() Config {
	return Config{
		Port:      cmp.Or(os.Getenv("PORT"), "3000"),
		DataDir:   cmp.Or(os.Getenv("DATA_DIR"), "data"),
		AppHosts:  hostList(cmp.Or(os.Getenv("APP_HOSTS"), "localhost")),
		AppDomain: normalizeHost(os.Getenv("APP_DOMAIN")),
		SCDAPIURL: cmp.Or(os.Getenv("SCD_API_URL"),
			"https://app.saascustomdomains.com"),
		SCDAPIToken:    os.Getenv("SCD_API_TOKEN"),
		SCDAccount:     os.Getenv("SCD_ACCOUNT_UUID"),
		SCDUpstream:    os.Getenv("SCD_UPSTREAM_UUID"),
		ProxyAuthToken: os.Getenv("PROXY_AUTH_TOKEN"),
		WebhookToken:   os.Getenv("SCD_WEBHOOK_TOKEN"),
	}
}

func hostList(s string) []string {
	var hosts []string
	for h := range strings.SplitSeq(s, ",") {
		if h = normalizeHost(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(configFromEnv()); err != nil {
		slog.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func newApp(cfg Config) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	store, err := openStore(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	scd := &SCDClient{
		BaseURL:  strings.TrimSuffix(cfg.SCDAPIURL, "/"),
		Token:    cfg.SCDAPIToken,
		Account:  cfg.SCDAccount,
		Upstream: cfg.SCDUpstream,
		HTTP:     http.DefaultClient,
	}
	return &App{
		cfg:     cfg,
		hosts:   Hosts{App: cfg.AppHosts, Domain: cfg.AppDomain},
		store:   store,
		domains: &DomainService{store: store, scd: scd},
	}, nil
}

func run(cfg Config) error {
	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := newApp(cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", srv.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(),
		10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// healthcheck is for Docker's HEALTHCHECK: the runtime image has no
// curl.
func healthcheck() int {
	port := cmp.Or(os.Getenv("PORT"), "3000")
	res, err := http.Get("http://127.0.0.1:" + port + "/up")
	if err != nil || res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
