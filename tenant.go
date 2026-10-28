package main

import (
	"cmp"
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net"
	"net/http"
)

type brandKey struct{}

// brandFrom returns the brand that hostRouter resolved for this request.
// Handlers on customer domains read it from here, never from the form,
// the path or a header.
func brandFrom(ctx context.Context) Brand {
	return ctx.Value(brandKey{}).(Brand)
}

// hostRouter sends requests for our own hostnames to app and requests
// for customer domains to tenant. ServeMux can't do this by itself: its
// host patterns are fixed strings, and customer domains come from the
// database.
func (a *App) hostRouter(app, tenant http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Behind SaaS Custom Domains, Host is our own hostname and the
		// customer's domain is in X-Served-For. Only trust it with the
		// upstream's auth token, or anyone who can reach the app could
		// claim any customer's domain.
		servedFor := r.Header.Get("X-Served-For")
		if servedFor != "" && !a.fromProxy(r) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		host := normalizeHost(cmp.Or(servedFor, r.Host))
		if a.hosts.IsApp(host) {
			app.ServeHTTP(w, r)
			return
		}

		brand, err := a.store.BrandByActiveHost(r.Context(), host)
		if errors.Is(err, errNotFound) {
			http.Error(w, "Unknown domain", http.StatusNotFound)
			return
		}
		if err != nil {
			slog.Error("look up host", "host", host, "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		r = r.WithContext(context.WithValue(r.Context(), brandKey{}, brand))
		if servedFor != "" {
			// Host is our own hostname here. Make it the customer's
			// domain, now that the token proved where the request came
			// from: http.CrossOriginProtection compares the browser's
			// Origin with r.Host, and would reject the form otherwise.
			r.Host = host
		}
		tenant.ServeHTTP(w, r)
	})
}

// fromProxy reports whether the request carries the upstream's auth
// token. An unset PROXY_AUTH_TOKEN rejects everything.
func (a *App) fromProxy(r *http.Request) bool {
	return secureCompare(r.Header.Get("X-SaaS-Domains-Auth-Token"), a.cfg.ProxyAuthToken)
}

// clientIP is the visitor's address. Behind the proxy, RemoteAddr is the
// proxy, and the visitor is in X-SaaS-Domains-IP.
func (a *App) clientIP(r *http.Request) string {
	if ip := r.Header.Get("X-SaaS-Domains-IP"); ip != "" && a.fromProxy(r) {
		return ip
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

// secureCompare compares in constant time, so a wrong guess doesn't
// reveal how many leading characters were right. An empty expected
// value never matches.
func secureCompare(given, expected string) bool {
	return expected != "" && subtle.ConstantTimeCompare([]byte(given), []byte(expected)) == 1
}
