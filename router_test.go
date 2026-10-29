package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	testProxyToken   = "test_proxy_token"
	testWebhookToken = "test_webhook_token"
)

// newTestApp has go.acme.test active for Acme and go.globex.test pending
// for Globex.
func newTestApp(t *testing.T, opts ...func(*Config)) *App {
	t.Helper()
	cfg := Config{
		DataDir:        t.TempDir(),
		AppHosts:       []string{"localhost", "app"},
		AppDomain:      "snip.example",
		ProxyAuthToken: testProxyToken,
		WebhookToken:   testWebhookToken,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	app, err := newApp(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.store.db.Close() })
	app.domains.scd = fakeAPI(t, okAPI)

	ctx := context.Background()
	acme, err := app.store.InsertDomain(ctx, 1, "go.acme.test")
	if err != nil {
		t.Fatal(err)
	}
	app.store.SetStatus(ctx, acme.ID, "active")
	app.store.SetProviderUUID(ctx, acme.ID, "dom_acme")
	globex, _ := app.store.InsertDomain(ctx, 2, "go.globex.test")
	app.store.SetProviderUUID(ctx, globex.ID, "dom_globex")
	return app
}

func request(method, host, path string, headers ...string) *http.Request {
	var body *strings.Reader
	if i := strings.Index(path, " "); i > 0 { // "path form-body"
		path, body = path[:i], strings.NewReader(path[i+1:])
	}
	var r *http.Request
	if body != nil {
		r = httptest.NewRequest(method, path, body)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = host
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	return r
}

func serve(app *App, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	return w
}

// What SaaS Custom Domains adds to a request for go.acme.test.
func viaProxy(host, token string) []string {
	return []string{
		"X-Served-For", host,
		"X-SaaS-Domains-Auth-Token", token,
		"X-SaaS-Domains-IP", "203.0.113.7",
		"X-Forwarded-Host", "app:3000",
		"X-Forwarded-Proto", "https",
	}
}

func TestAppHostServesTheDashboard(t *testing.T) {
	app := newTestApp(t)
	w := serve(app, request("GET", "localhost:3000", "/"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Snip dashboard") {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
}

func TestTenantRoutesAreHiddenOnTheAppHost(t *testing.T) {
	app := newTestApp(t)
	if w := serve(app, request("GET", "localhost:3000", "/launch")); w.Code != 404 {
		t.Errorf("GET /launch on the app host: got %d, want 404", w.Code)
	}
	if w := serve(app, request("POST", "localhost:3000", "/report link=x")); w.Code != 404 {
		t.Errorf("POST /report on the app host: got %d, want 404", w.Code)
	}
}

func TestDashboardIsHiddenOnCustomerDomains(t *testing.T) {
	app := newTestApp(t)
	w := serve(app, request("POST", "go.acme.test", "/brands/1/domains host=evil.test"))
	if w.Code != 404 {
		t.Errorf("got %d, want 404", w.Code)
	}
}

func TestActiveDomainRedirects(t *testing.T) {
	app := newTestApp(t)
	w := serve(app, request("GET", "go.acme.test", "/launch"))
	if w.Code != 302 || w.Header().Get("Location") != "https://example.com/acme/launch-day" {
		t.Fatalf("got %d %q", w.Code, w.Header().Get("Location"))
	}
}

// r.Host is "go.acme.test:3000" when the browser uses a port.
func TestHostWithPortFindsTheBrand(t *testing.T) {
	app := newTestApp(t)
	if w := serve(app, request("GET", "go.acme.test:3000", "/launch")); w.Code != 302 {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
}

func TestUnknownSlugIs404OnTheBrandsDomain(t *testing.T) {
	app := newTestApp(t)
	w := serve(app, request("GET", "go.acme.test", "/nope"))
	if w.Code != 404 || !strings.Contains(w.Body.String(), "No short link at go.acme.test/nope") {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
}

func TestPendingAndUnknownDomainsAre404(t *testing.T) {
	app := newTestApp(t)
	for _, host := range []string{"go.globex.test", "evil.test"} {
		w := serve(app, request("GET", host, "/careers"))
		if w.Code != 404 || !strings.Contains(w.Body.String(), "Unknown domain") {
			t.Errorf("%s: got %d %q", host, w.Code, w.Body)
		}
	}
}

func TestXServedForWithTheProxyToken(t *testing.T) {
	app := newTestApp(t)
	w := serve(app, request("GET", "app:3000", "/launch", viaProxy("go.acme.test", testProxyToken)...))
	if w.Code != 302 {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
}

func TestXServedForWithoutTheProxyTokenIs403(t *testing.T) {
	cases := map[string]struct {
		token, expected string
	}{
		"no token":         {"", testProxyToken},
		"wrong token":      {"guess", testProxyToken},
		"token not set up": {"", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			app := newTestApp(t, func(cfg *Config) { cfg.ProxyAuthToken = c.expected })
			w := serve(app, request("GET", "localhost", "/launch", viaProxy("go.acme.test", c.token)...))
			if w.Code != 403 {
				t.Fatalf("got %d, want 403", w.Code)
			}
		})
	}
}

// Nothing in net/http reads X-Forwarded-Host, and neither does this app.
func TestXForwardedHostIsIgnored(t *testing.T) {
	app := newTestApp(t)
	w := serve(app, request("GET", "localhost", "/", "X-Forwarded-Host", "go.acme.test"))
	if !strings.Contains(w.Body.String(), "Snip dashboard") {
		t.Fatalf("X-Forwarded-Host picked the tenant: %d %q", w.Code, w.Body)
	}
}

// Load balancers call the health check by IP address.
func TestHealthCheckSkipsTheHostCheck(t *testing.T) {
	app := newTestApp(t)
	if w := serve(app, request("GET", "10.0.0.5:3000", "/up")); w.Code != 200 {
		t.Errorf("/up by IP: got %d", w.Code)
	}
	if w := serve(app, request("GET", "10.0.0.5:3000", "/")); w.Code != 404 {
		t.Errorf("/ by IP: got %d, want 404", w.Code)
	}
}

// The browser on go.acme.test sends Origin: https://go.acme.test, and the
// request reaches us with Host: app:3000. Without the r.Host rewrite in
// hostRouter, http.CrossOriginProtection rejects it with 403 "cross-origin
// request detected, and/or browser is out of date: Sec-Fetch-Site is
// missing, and Origin does not match Host".
func TestFormPostThroughTheProxy(t *testing.T) {
	app := newTestApp(t)
	headers := append(viaProxy("go.acme.test", testProxyToken), "Origin", "https://go.acme.test")
	w := serve(app, request("POST", "app:3000", "/report link=go.acme.test/old", headers...))
	if w.Code != 303 || w.Header().Get("Location") != "/?reported" {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}

	var ip string
	app.store.db.QueryRow(`SELECT client_ip FROM reports WHERE brand_id = 1`).Scan(&ip)
	if ip != "203.0.113.7" {
		t.Errorf("client_ip = %q, want the visitor's IP from X-SaaS-Domains-IP", ip)
	}
}

func TestCrossSiteFormPostIsRejected(t *testing.T) {
	app := newTestApp(t)
	for _, headers := range [][]string{
		{"Sec-Fetch-Site", "cross-site"},
		{"Origin", "https://evil.example"},
	} {
		w := serve(app, request("POST", "go.acme.test", "/report link=x", headers...))
		if w.Code != 403 {
			t.Errorf("%v: got %d, want 403", headers, w.Code)
		}
	}
}

func TestAddDomain(t *testing.T) {
	app := newTestApp(t)
	add := func(host string) *httptest.ResponseRecorder {
		return serve(app, request("POST", "localhost", "/brands/1/domains host="+url.QueryEscape(host)))
	}

	for host, msg := range map[string]string{
		"admin.snip.example": "That&#39;s one of our domains.",
		"localhost":          "That isn&#39;t a valid domain.",
		"not a domain":       "That isn&#39;t a valid domain.",
		"go.acme.test":       "go.acme.test is already connected to a brand.",
		"GO.GLOBEX.TEST.":    "go.globex.test is already connected to a brand.",
	} {
		w := add(host)
		if w.Code != 422 || !strings.Contains(w.Body.String(), msg) {
			t.Errorf("%q: got %d, want 422 with %q", host, w.Code, msg)
		}
	}

	// A new domain stays pending until the API says DNS is right.
	if w := add("go.initech.test"); w.Code != 303 {
		t.Fatalf("got %d %q", w.Code, w.Body)
	}
	if w := serve(app, request("GET", "go.initech.test", "/launch")); w.Code != 404 {
		t.Errorf("pending domain: got %d, want 404", w.Code)
	}
}

func TestRemoveDomain(t *testing.T) {
	app := newTestApp(t)
	d, _ := app.store.DomainByHost(context.Background(), "go.acme.test")
	w := serve(app, request("POST", "localhost", "/domains/"+itoa(d.ID)+"/remove"))
	if w.Code != 303 {
		t.Fatalf("got %d", w.Code)
	}
	if w := serve(app, request("GET", "go.acme.test", "/launch")); w.Code != 404 {
		t.Errorf("removed domain: got %d, want 404", w.Code)
	}
}
