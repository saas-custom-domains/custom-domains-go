package main

// These tests pin down the net/http behavior the guide describes. They
// test the standard library, not this app, so a Go release that changes
// any of it shows up in the weekly CI run.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

func TestGotchaRHostIncludesThePort(t *testing.T) {
	var host string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host = r.Host
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Host = "go.acme.localhost:3000"
	srv.Client().Do(req)
	if host != "go.acme.localhost:3000" {
		t.Fatalf("r.Host = %q", host)
	}
}

func TestGotchaServeMuxHostPatternsAreFixed(t *testing.T) {
	// A wildcard in the host panics when you register it.
	err := func() (err any) {
		defer func() { err = recover() }()
		http.NewServeMux().HandleFunc("{tenant}.snip.example/", func(http.ResponseWriter, *http.Request) {})
		return nil
	}()
	want := `parsing "{tenant}.snip.example/": at offset 0: host contains '{' (missing initial '/'?)`
	if fmt.Sprint(err) != want {
		t.Errorf("panic = %v", err)
	}

	// "*" is taken literally, so this pattern never matches anything.
	mux := http.NewServeMux()
	mux.HandleFunc("*.snip.example/", func(http.ResponseWriter, *http.Request) {})
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "acme.snip.example"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Errorf("*.snip.example matched acme.snip.example: %d", w.Code)
	}
}

func TestGotchaCrossOriginProtectionComparesOriginWithHost(t *testing.T) {
	csrf := http.NewCrossOriginProtection()

	// What reaches the app behind SaaS Custom Domains when the browser
	// doesn't send Sec-Fetch-Site (it only does over HTTPS or localhost,
	// and browsers from before 2023 never do).
	r := httptest.NewRequest("POST", "/report", nil)
	r.Host = "app.snip.example"
	r.Header.Set("Origin", "https://go.acme.com")
	want := "cross-origin request detected, and/or browser is out of date: " +
		"Sec-Fetch-Site is missing, and Origin does not match Host"
	if err := csrf.Check(r); err == nil || err.Error() != want {
		t.Errorf("Check() = %v", err)
	}

	r.Host = "go.acme.com" // what hostRouter does after the token check
	if err := csrf.Check(r); err != nil {
		t.Errorf("Check() with the customer's Host = %v", err)
	}

	// With Sec-Fetch-Site, Host doesn't matter.
	r.Host = "app.snip.example"
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	if err := csrf.Check(r); err != nil {
		t.Errorf("Check() with Sec-Fetch-Site = %v", err)
	}
}

// httputil.NewSingleHostReverseProxy still uses the deprecated Director,
// which passes the client's X-Forwarded-* headers through. Put one in
// front of an app that trusts X-Forwarded-Host and any visitor can pick
// the tenant.
func TestGotchaReverseProxyPassesClientForwardedHeaders(t *testing.T) {
	var got http.Header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
	}))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)

	send := func(proxy http.Handler) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = "go.acme.com"
		r.Header.Set("X-Forwarded-Host", "go.victim.com")
		r.Header.Set("X-Served-For", "go.victim.com")
		proxy.ServeHTTP(httptest.NewRecorder(), r)
	}

	send(httputil.NewSingleHostReverseProxy(target))
	if got.Get("X-Forwarded-Host") != "go.victim.com" {
		t.Errorf("Director proxy: X-Forwarded-Host = %q", got.Get("X-Forwarded-Host"))
	}

	send(&httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(target)
		pr.SetXForwarded()
	}})
	if got.Get("X-Forwarded-Host") != "go.acme.com" {
		t.Errorf("Rewrite proxy: X-Forwarded-Host = %q", got.Get("X-Forwarded-Host"))
	}
	// Neither strips headers it doesn't know about. That's why the app
	// needs the token, not just X-Served-For.
	if !strings.Contains(got.Get("X-Served-For"), "go.victim.com") {
		t.Errorf("Rewrite proxy dropped X-Served-For")
	}
}
