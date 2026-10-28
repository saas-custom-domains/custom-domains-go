# Custom domains in Go

An example Go app, standard library `net/http` only, where each customer serves its links on its own domain. It's a small link shortener, Snip: every brand has short links, and its owner can use `go.theircompany.com/launch` instead of a Snip URL.

It goes with the guide [How to Add Custom Domains to a Go App](https://saascustomdomains.com/blog/posts/custom-domains-go).

What's in it:

- `hostRouter` in `tenant.go` finds the brand from the request's hostname, puts it in the request's `context.Context`, and sends the request to the customer-site `ServeMux`. Unknown and unverified domains get a 404.
- Requests from the [SaaS Custom Domains](https://saascustomdomains.com) proxy are accepted only with the upstream's auth token, so nobody can claim a customer's domain by sending `X-Served-For` themselves.
- A "report a broken link" form on each customer domain, protected by Go's `http.CrossOriginProtection`, that works through the proxy (see [Forms and `http.CrossOriginProtection`](#forms-and-httpcrossoriginprotection)).
- A dashboard to add a domain, check its DNS and remove it. Webhooks update the status.
- Three ways to run HTTPS: SaaS Custom Domains (default), your own Caddy with on-demand TLS, or TLS in the Go binary itself with `certmagic`.
- Dependencies: the standard library, `modernc.org/sqlite` (pure Go, no cgo) and `certmagic` for the optional TLS mode.

## Run it in 2 minutes

You need Docker.

```bash
git clone https://github.com/saas-custom-domains/custom-domains-go
cd custom-domains-go
docker compose up --build
```

1. Open http://localhost:3000 and add `go.acme.localhost` to Acme Rockets.
2. Within a few seconds the status changes to **Live**: the mock API "found" the DNS records and sent a webhook.
3. Open http://go.acme.localhost/launch. The request goes through the mock proxy on port 80, exactly like a real customer domain goes through SaaS Custom Domains, and you're redirected to Acme's launch page. Open http://go.acme.localhost/ for the brand page and the report form.

`*.localhost` always resolves to your own machine in Chrome, Firefox, Safari and curl, so there's nothing to add to `/etc/hosts`.

The mock is [scd-mock](https://github.com/saas-custom-domains/scd-mock): a local copy of the SaaS Custom Domains API and proxy, so you can try everything without an account.

### Without Docker

You need Go 1.27. With an older `go` on your path, the `toolchain` line in `go.mod` downloads 1.27 for you.

```bash
cp .env.example .env
set -a; . ./.env; set +a
DOMAINS_PROVIDER=caddy go run .
```

Add a domain such as `go.acme.localhost` and open http://go.acme.localhost:3000/launch. `DOMAINS_PROVIDER=caddy` means adding a domain doesn't call the API, and `*.localhost` domains count as pointed at you.

## How it works

```
Browser ── go.acme.com ──▶ SaaS Custom Domains ──▶ your Go app
                           (TLS certificate,       Host: app.snip.example
                            proxy)                 X-Served-For: go.acme.com
                                                   X-SaaS-Domains-Auth-Token: …
```

The proxy sends your app's own hostname in `Host` and `X-Forwarded-Host`, the way hosting platforms and most apps expect, and the customer's domain separately in `X-Served-For`.

| File | What it does |
| --- | --- |
| `tenant.go` | Checks the proxy token, picks the host, resolves the brand, puts it in the context |
| `handlers.go` | The two `ServeMux`es (dashboard and customer sites), the webhook, Caddy's ask endpoint |
| `hosts.go` | Host normalization, validation, `APP_HOSTS` and the reserved domain |
| `store.go` | SQLite tables and queries, with a unique index on the host |
| `domains.go` | Add, check and remove domains with either provider, plus the DIY DNS check |
| `scd.go` | API client for SaaS Custom Domains, with `net/http` |
| `tls.go` | Optional on-demand TLS in the binary with `certmagic` |
| `gotchas_test.go` | Pins the `net/http` behavior described below |

## Go gotchas

Each one has a test, so the weekly CI run notices if a Go release changes the behavior.

### `r.Host` includes the port

On `http://go.acme.localhost:3000`, `r.Host` is `go.acme.localhost:3000`. Look that up in the database and you get `404 Unknown domain`. `ServeMux` strips the port when it matches host patterns, which makes it easy to assume `r.Host` doesn't have one. `normalizeHost` lowercases, drops a numeric port and a trailing dot before every lookup, and domains are stored the same way. Test: `TestHostWithPortFindsTheBrand`.

### `ServeMux` host patterns are fixed strings

Go 1.22 patterns can include a host (`go.acme.com/`), but only a literal one. A wildcard panics when you register it:

```
parsing "{tenant}.snip.example/": at offset 0: host contains '{' (missing initial '/'?)
```

`*.snip.example/` is worse: it registers without complaint and never matches. Customer domains come from your database anyway, so `hostRouter` decides per request and hands off to one of two muxes: the dashboard's or the customer sites'. Customer routes can't be reached on your own hostname, and the dashboard can't be reached on a customer's. Tests: `TestGotchaServeMuxHostPatternsAreFixed`, `TestTenantRoutesAreHiddenOnTheAppHost`, `TestDashboardIsHiddenOnCustomerDomains`.

### Forms and `http.CrossOriginProtection`

Go 1.25 added `http.CrossOriginProtection` for CSRF. It trusts the browser's `Sec-Fetch-Site` header when there is one. When there isn't (browsers from before 2023, and any page served over plain HTTP from a name other than `localhost`), it compares the `Origin` header with `r.Host`. Behind the proxy `r.Host` is your app's hostname, so the form on `go.acme.com` gets a 403:

```
cross-origin request detected, and/or browser is out of date: Sec-Fetch-Site is missing, and Origin does not match Host
```

Over HTTPS, current browsers send `Sec-Fetch-Site: same-origin` and the form works. Through the local mock, which is plain HTTP, Chromium doesn't send it, and the end-to-end test fails with the error above.

The same goes for anything else that needs the customer's domain: links you print (`app:3000/launch` instead of `go.acme.com/launch`), absolute redirects, canonical URLs. `hostRouter` gives it to the rest of the app: once the token check passes, it sets `r.Host` to the domain from `X-Served-For`, before `CrossOriginProtection` runs. `AddTrustedOrigin` doesn't help here, because it takes a fixed list and your customers' domains aren't fixed. Tests: `TestFormPostThroughTheProxy` (fails without the `r.Host` line), `TestGotchaCrossOriginProtectionComparesOriginWithHost`, and the end-to-end test.

(SaaS Custom Domains support can also put the customer's domain in `X-Forwarded-Host` for your upstream.)

### `X-Forwarded-*` headers are not trusted, unless a proxy does it for you

Nothing in `net/http` reads `X-Forwarded-Host`, `X-Forwarded-Proto` or `X-Forwarded-For`, and this app doesn't either. If you put your own Go proxy in front, check how it's built: `httputil.NewSingleHostReverseProxy` still uses the deprecated `Director`, which passes the client's `X-Forwarded-Host` straight through. An app that picks the tenant from that header then lets any visitor pick it. `ReverseProxy` with `Rewrite` strips incoming `X-Forwarded-*` headers, but neither strips `X-Served-For`, which is why the app checks the token with `subtle.ConstantTimeCompare` instead of trusting the header. Tests: `TestGotchaReverseProxyPassesClientForwardedHeaders`, `TestXForwardedHostIsIgnored`, `TestXServedForWithoutTheProxyTokenIs403`.

The visitor's IP is in `X-SaaS-Domains-IP` (`r.RemoteAddr` is the proxy). `clientIP` reads it only after the same token check.

### Health checks and webhooks don't arrive on your domain

Load balancers and Docker call the health check by IP address, and the mock's webhooks arrive as `Host: app:3000`. Both look like unknown customer domains to a host check. `/up` is registered on the outer mux, before `hostRouter`, and `APP_HOSTS` lists every internal name (`app` in `compose.yaml`). Tests: `TestHealthCheckSkipsTheHostCheck`, the webhook tests.

### The brand comes from the context, not the form

`hostRouter` stores the brand under an unexported key type, so no other package can overwrite it, and handlers call `brandFrom(r.Context())`. The report form never says which brand it's for: a form posted to `go.acme.com` can only ever be about Acme.

## Run your own proxy instead (Caddy)

```bash
DOMAINS_PROVIDER=caddy docker compose --profile diy up --build
```

Add `go.acme.localhost` in the dashboard, then:

```bash
curl -k https://go.acme.localhost/launch
```

Caddy asks `/caddy/ask` before issuing a certificate, and only active domains get one. Locally it uses Caddy's internal CA, hence `-k`. In production you'd remove `internal` from the `Caddyfile` and run it on a server with a static IP.

## Or terminate TLS in Go (certmagic)

Set `HTTPS_ADDR` and the binary serves HTTPS itself with [certmagic](https://github.com/caddyserver/certmagic), the library inside Caddy. Certificates are requested on demand, during the first TLS handshake for a domain, and the `DecisionFunc` allows only active domains (the same check as Caddy's ask endpoint). The plain HTTP listener answers ACME HTTP-01 challenges.

```bash
docker compose -f compose.yaml -f compose.certmagic.yaml up --build
```

Add `go.acme.localhost`, then:

```bash
curl -k https://go.acme.localhost/launch
```

Locally the CA is [Pebble](https://github.com/letsencrypt/pebble), Let's Encrypt's test server, with domain validation switched off because it can't reach your machine. That exercises the whole on-demand path: the decision function, the ACME order, storage, and the dashboard turning **Live** when certmagic reports `cert_obtained`. A domain that isn't active gets no certificate, and the app logs `certificate is not allowed for server name evil.localhost: decision func: evil.localhost is not an active custom domain`. `dev/pebble.minica.pem` is Pebble's public test root, so the app trusts Pebble's API.

What Pebble can't show is real validation: in production, ports 80 and 443 must be reachable from the internet, and your customers' DNS must point at your server. Try `ACME_CA=https://acme-staging-v02.api.letsencrypt.org/directory` on a server with a real domain first.

After that, the job is yours: certificate storage shared by every instance (certmagic's default is the local disk), renewals, Let's Encrypt rate limits, a static IP or anycast for root domains, and abuse from domains pointed at you. That's what SaaS Custom Domains does for you.

## Use it with your SaaS Custom Domains account

1. [Create an API token](https://app.saascustomdomains.com/api_tokens) and an upstream pointing at your deployed app.
2. Set the environment variables from `.env.example`: `SCD_API_TOKEN`, `SCD_ACCOUNT_UUID`, `SCD_UPSTREAM_UUID`, `PROXY_AUTH_TOKEN` (the upstream's auth token), `SCD_WEBHOOK_TOKEN` (from the webhook you create in the dashboard), and `APP_HOSTS`.
3. Remove `SCD_API_URL` so the client talks to the real API.

`APP_HOSTS` must list every hostname your own traffic arrives on: your app's domain, plus any internal name your platform uses to reach the app. Any other host is treated as a customer domain and gets a 404 if it isn't connected.

Your customers point a CNAME at `in.saascustomdomains.com` (or A records at our two static IPs for a root domain). See the [docs](https://docs.saascustomdomains.com).

## Tests

```bash
go test ./...                  # unit tests: routing, tokens, webhooks, API client, net/http gotchas
docker compose up -d --build --wait
cd e2e
npm ci
npx playwright install chromium
npm test                       # end to end, through the mock proxy
```

The end-to-end tests use `*.test` domains and send the browser's requests for them to the mock proxy, so nothing depends on DNS or on port 80. To run a second stack side by side, set `APP_PORT`, `SCD_MOCK_API_PORT` and `SCD_MOCK_PROXY_PORT` for both `docker compose` and `npm test`.

CI runs both on every push and once a week, against the latest stable Go.

## Tested with

| | Version |
| --- | --- |
| Go | 1.27.3 |
| modernc.org/sqlite | 1.61.0 |
| certmagic | 0.25.6 |
| Caddy | 2.11.7 |
| Pebble | 2.10.1 |
| Playwright | 1.64.1 |

Last checked: 2026-10-28.

## Not production-ready on purpose

There's no login. In your app, check in every dashboard handler that the signed-in user owns the brand and the domain. SQLite keeps the example small; the queries are plain SQL, so use your own database.

## License

MIT
