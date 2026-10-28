package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/caddyserver/certmagic"
)

// onDemandTLS terminates TLS in this binary, the do-it-yourself
// alternative to Caddy or SaaS Custom Domains. A certificate is
// requested during the first TLS handshake for a domain, and only if
// allowCertificate says the domain is active. It returns the HTTPS
// server and a handler for the plain HTTP port that also answers
// ACME HTTP-01 challenges.
func (a *App) onDemandTLS(
	h http.Handler,
) (*http.Server, http.Handler, error) {
	certmagic.Default.Storage = &certmagic.FileStorage{
		Path: filepath.Join(a.cfg.DataDir, "certmagic"),
	}
	magic := certmagic.NewDefault()
	magic.OnDemand = &certmagic.OnDemandConfig{
		DecisionFunc: a.allowCertificate,
	}
	magic.OnEvent = a.onCertEvent

	issuer := certmagic.ACMEIssuer{
		CA:     certmagic.LetsEncryptProductionCA,
		Email:  a.cfg.ACMEEmail,
		Agreed: true,
	}
	if a.cfg.ACMECA != "" {
		issuer.CA = a.cfg.ACMECA
	}
	if a.cfg.ACMECARoot != "" {
		// For a local ACME server such as Pebble, whose API uses a
		// certificate from its own CA.
		pem, err := os.ReadFile(a.cfg.ACMECARoot)
		if err != nil {
			return nil, nil, err
		}
		issuer.TrustedRoots = x509.NewCertPool()
		issuer.TrustedRoots.AppendCertsFromPEM(pem)
	}
	acme := certmagic.NewACMEIssuer(magic, issuer)
	magic.Issuers = []certmagic.Issuer{acme}

	tlsConfig := magic.TLSConfig()
	tlsConfig.NextProtos = append(
		[]string{"h2", "http/1.1"}, tlsConfig.NextProtos...,
	)
	srv := &http.Server{
		Addr:      a.cfg.HTTPSAddr,
		Handler:   h,
		TLSConfig: tlsConfig,
	}
	return srv, acme.HTTPChallengeHandler(h), nil
}

// allowCertificate is certmagic's DecisionFunc, the same check as
// Caddy's ask endpoint. Without it, anyone could point a domain at
// your server and make it request certificates until the CA
// rate-limits you.
func (a *App) allowCertificate(ctx context.Context, name string) error {
	ok, err := a.store.IsActiveHost(ctx, normalizeHost(name))
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s is not an active custom domain", name)
	}
	return nil
}

// onCertEvent marks a domain as Live once its certificate is issued.
func (a *App) onCertEvent(
	ctx context.Context, event string, data map[string]any,
) error {
	if event != "cert_obtained" {
		return nil
	}
	host, _ := data["identifier"].(string)
	d, err := a.store.DomainByHost(ctx, normalizeHost(host))
	if err == nil {
		err = a.store.SetHTTPSReady(ctx, d.ID)
	}
	if err != nil {
		slog.Error("mark https ready", "host", host, "err", err)
	}
	return nil
}
