package main

import (
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

//go:embed templates/*.html
var templateFS embed.FS

var templates = template.Must(template.New("").Funcs(template.FuncMap{
	"statusLabel": statusLabel,
}).ParseFS(templateFS, "templates/*.html"))

type App struct {
	cfg     Config
	hosts   Hosts
	store   *Store
	domains *DomainService
}

func (a *App) Handler() http.Handler {
	// Rejects cross-site form posts (CSRF). It compares the browser's
	// Origin with r.Host when Sec-Fetch-Site is missing, which is why
	// hostRouter sets r.Host to the customer's domain.
	csrf := http.NewCrossOriginProtection()

	// The dashboard and the endpoints called by the proxy and by Caddy.
	app := http.NewServeMux()
	app.HandleFunc("GET /{$}", a.dashboard)
	app.HandleFunc("POST /brands/{id}/domains", a.addDomain)
	app.HandleFunc("POST /domains/{id}/check", a.checkDomain)
	app.HandleFunc("POST /domains/{id}/remove", a.removeDomain)
	app.HandleFunc("POST /webhooks/saascustomdomains", a.webhook)
	app.HandleFunc("GET /caddy/ask", a.caddyAsk)

	// Customer domains: go.acme.com/launch redirects.
	tenant := http.NewServeMux()
	tenant.HandleFunc("GET /{$}", a.brandHome)
	tenant.HandleFunc("GET /{slug}", a.followLink)
	tenant.HandleFunc("POST /report", a.report)

	root := http.NewServeMux()
	// Load balancers and Docker call this by IP address, so it skips
	// the host check.
	root.HandleFunc("GET /up", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	root.Handle("/", a.hostRouter(csrf.Handler(app), csrf.Handler(tenant)))
	return root
}

// Customer domains

func (a *App) brandHome(w http.ResponseWriter, r *http.Request) {
	a.renderBrand(w, r, http.StatusOK, "")
}

func (a *App) renderBrand(w http.ResponseWriter, r *http.Request, status int, alert string) {
	brand := brandFrom(r.Context())
	links, err := a.store.Links(r.Context(), brand.ID)
	if err != nil {
		serverError(w, err)
		return
	}
	render(w, status, "brand.html", map[string]any{
		"Brand":    brand,
		"Host":     r.Host, // the customer's domain, set by hostRouter
		"Links":    links,
		"Reported": r.URL.Query().Has("reported"),
		"Alert":    alert,
	})
}

func (a *App) followLink(w http.ResponseWriter, r *http.Request) {
	brand := brandFrom(r.Context())
	url, err := a.store.LinkURL(r.Context(), brand.ID, r.PathValue("slug"))
	if errors.Is(err, errNotFound) {
		render(w, http.StatusNotFound, "notfound.html", map[string]any{
			"Brand": brand, "Host": r.Host, "Path": r.URL.Path,
		})
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	// 302, not 301: browsers cache a 301 forever, and links get edited.
	http.Redirect(w, r, url, http.StatusFound)
}

// report is the form visitors post on the customer's domain. The brand
// comes from the request's host, never from the form.
func (a *App) report(w http.ResponseWriter, r *http.Request) {
	brand := brandFrom(r.Context())
	link := strings.TrimSpace(r.FormValue("link"))
	if link == "" {
		a.renderBrand(w, r, http.StatusUnprocessableEntity, "Tell us which link is broken.")
		return
	}
	if err := a.store.AddReport(r.Context(), brand.ID, link, a.clientIP(r)); err != nil {
		serverError(w, err)
		return
	}
	// A relative URL, so the browser stays on the customer's domain.
	http.Redirect(w, r, "/?reported", http.StatusSeeOther)
}

// Dashboard. This example has no login: in your app, check that the
// signed-in user owns the brand and the domain in every handler.

type brandView struct {
	Brand
	Domains []Domain
	Reports int
	Error   string
	Input   string
}

func (a *App) dashboard(w http.ResponseWriter, r *http.Request) {
	a.renderDashboard(w, r, http.StatusOK, 0, "")
}

func (a *App) renderDashboard(w http.ResponseWriter, r *http.Request, status int, errBrand int64, errMsg string) {
	ctx := r.Context()
	brands, err := a.store.Brands(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	domains, err := a.store.Domains(ctx)
	if err != nil {
		serverError(w, err)
		return
	}
	views := make([]brandView, len(brands))
	for i, b := range brands {
		views[i].Brand = b
		for _, d := range domains {
			if d.BrandID == b.ID {
				views[i].Domains = append(views[i].Domains, d)
			}
		}
		if views[i].Reports, err = a.store.ReportCount(ctx, b.ID); err != nil {
			serverError(w, err)
			return
		}
		if b.ID == errBrand {
			views[i].Error = errMsg
			views[i].Input = r.FormValue("host")
		}
	}
	render(w, status, "dashboard.html", views)
}

func (a *App) addDomain(w http.ResponseWriter, r *http.Request) {
	brandID, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	host := normalizeHost(r.FormValue("host"))
	dnsContact := strings.TrimSpace(r.FormValue("dns_contact"))

	fail := func(msg string) {
		a.renderDashboard(w, r, http.StatusUnprocessableEntity, brandID, msg)
	}
	if !isValidHost(host) {
		fail("That isn't a valid domain.")
		return
	}
	if a.hosts.IsReserved(host) {
		fail("That's one of our domains.")
		return
	}

	_, err := a.domains.Add(r.Context(), brandID, host, dnsContact)
	if errors.Is(err, errHostTaken) {
		fail(host + " is already connected to a brand.")
		return
	}
	if apiErr, ok := errors.AsType[*APIError](err); ok && apiErr.Code == "host_taken" {
		fail(host + " is already connected to another SaaS Custom Domains account.")
		return
	}
	if err != nil {
		slog.Error("add domain", "host", host, "err", err)
		fail("Could not add the domain. Try again.")
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *App) checkDomain(w http.ResponseWriter, r *http.Request) {
	a.withDomain(w, r, func(d Domain) error {
		_, err := a.domains.Check(r.Context(), d)
		return err
	})
}

func (a *App) removeDomain(w http.ResponseWriter, r *http.Request) {
	a.withDomain(w, r, func(d Domain) error {
		return a.domains.Remove(r.Context(), d)
	})
}

func (a *App) withDomain(w http.ResponseWriter, r *http.Request, fn func(Domain) error) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	d, err := a.store.Domain(r.Context(), id)
	if err == nil {
		err = fn(d)
	}
	if err != nil && !errors.Is(err, errNotFound) {
		serverError(w, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// Webhooks from SaaS Custom Domains.
// Events: https://docs.saascustomdomains.com/webhooks
func (a *App) webhook(w http.ResponseWriter, r *http.Request) {
	if !secureCompare(r.URL.Query().Get("token"), a.cfg.WebhookToken) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	var payload struct {
		Event  string `json:"event"`
		Domain struct {
			UUID string `json:"uuid"`
		} `json:"domain"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&payload); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	d, err := a.store.DomainByProviderUUID(ctx, payload.Domain.UUID)
	if errors.Is(err, errNotFound) {
		// Not ours, or already removed: acknowledge so it isn't retried.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}

	// Each event sets a state rather than toggling one, so a repeated
	// delivery changes nothing.
	switch payload.Event {
	case "domain_dns_proxy_record_valid":
		err = a.store.SetStatus(ctx, d.ID, "active")
	case "domain_dns_proxy_record_invalid":
		err = a.store.SetStatus(ctx, d.ID, "pending")
	case "tls_certificate_issued":
		err = a.store.SetHTTPSReady(ctx, d.ID)
	case "domain_deleted":
		err = a.store.DeleteDomain(ctx, d.ID)
	}
	if err != nil {
		serverError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// caddyAsk is Caddy's on-demand TLS check (DOMAINS_PROVIDER=caddy).
// 200 means issue a certificate, anything else means refuse.
func (a *App) caddyAsk(w http.ResponseWriter, r *http.Request) {
	ok, err := a.store.IsActiveHost(r.Context(), normalizeHost(r.URL.Query().Get("domain")))
	switch {
	case err != nil:
		serverError(w, err)
	case ok:
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func statusLabel(d Domain) string {
	switch {
	case d.Status != "active":
		return "Waiting for DNS"
	case !d.HTTPSReady:
		return "DNS OK, waiting for HTTPS"
	default:
		return "Live"
	}
}

func render(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := templates.ExecuteTemplate(w, name, data); err != nil {
		slog.Error("render", "template", name, "err", err)
	}
}

func serverError(w http.ResponseWriter, err error) {
	slog.Error("request failed", "err", err)
	http.Error(w, "Internal Server Error", http.StatusInternalServerError)
}
