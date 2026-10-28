package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func webhook(token, event, uuid string) *http.Request {
	body := `{"event":"` + event + `","domain":{"uuid":"` + uuid + `","host":"x"}}`
	r := httptest.NewRequest("POST", "/webhooks/saascustomdomains?token="+token, strings.NewReader(body))
	// Webhooks reach the container on its internal name. Without "app"
	// in APP_HOSTS, they'd get "Unknown domain".
	r.Host = "app:3000"
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestWebhookNeedsTheToken(t *testing.T) {
	app := newTestApp(t)
	if w := serve(app, webhook("guess", "domain_dns_proxy_record_valid", "dom_globex")); w.Code != 401 {
		t.Errorf("wrong token: got %d, want 401", w.Code)
	}

	unset := newTestApp(t, func(cfg *Config) { cfg.WebhookToken = "" })
	if w := serve(unset, webhook("", "domain_dns_proxy_record_valid", "dom_globex")); w.Code != 401 {
		t.Errorf("token not set up: got %d, want 401", w.Code)
	}
}

func TestWebhookUpdatesTheStatus(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	status := func() string {
		d, _ := app.store.DomainByProviderUUID(ctx, "dom_globex")
		return statusLabel(d)
	}

	steps := []struct{ event, want string }{
		{"domain_dns_proxy_record_valid", "DNS OK, waiting for HTTPS"},
		{"domain_dns_proxy_record_valid", "DNS OK, waiting for HTTPS"}, // redelivery
		{"tls_certificate_issued", "Live"},
		{"domain_dns_proxy_record_invalid", "Waiting for DNS"},
	}
	for _, s := range steps {
		if w := serve(app, webhook(testWebhookToken, s.event, "dom_globex")); w.Code != 204 {
			t.Fatalf("%s: got %d", s.event, w.Code)
		}
		if got := status(); got != s.want {
			t.Fatalf("after %s: %q, want %q", s.event, got, s.want)
		}
	}

	serve(app, webhook(testWebhookToken, "domain_deleted", "dom_globex"))
	if _, err := app.store.DomainByProviderUUID(ctx, "dom_globex"); err != errNotFound {
		t.Errorf("domain_deleted kept the domain: %v", err)
	}
}

func TestWebhookForAnUnknownDomainIsAcknowledged(t *testing.T) {
	app := newTestApp(t)
	if w := serve(app, webhook(testWebhookToken, "domain_dns_proxy_record_valid", "dom_other")); w.Code != 204 {
		t.Errorf("got %d, want 204", w.Code)
	}
}
