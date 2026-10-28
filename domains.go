package main

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
)

// DomainService adds, checks and removes custom domains, keeping the
// database and the proxy in step. With SCD set, SaaS Custom Domains is
// the proxy. Without it you run your own (Caddy, or certmagic in this
// binary) and the app checks DNS itself.
type DomainService struct {
	store *Store
	scd   *SCDClient // nil when you run your own proxy
	dns   DNSCheck
}

func (s *DomainService) Add(
	ctx context.Context, brandID int64, host, dnsContact string,
) (Domain, error) {
	d, err := s.store.InsertDomain(ctx, brandID, host)
	if err != nil {
		return d, err
	}
	if s.scd == nil {
		// Your own proxy: check DNS now, and again with "Check DNS".
		_, err = s.Check(ctx, d)
		return d, err
	}

	res, err := s.scd.CreateDomain(ctx, host, dnsContact)
	if err != nil {
		// Don't keep a row the proxy doesn't know about.
		return d, errors.Join(err, s.store.DeleteDomain(ctx, d.ID))
	}
	d.ProviderUUID = res.UUID
	return d, s.store.SetProviderUUID(ctx, d.ID, res.UUID)
}

// Check asks for a DNS check now instead of waiting for the webhook.
func (s *DomainService) Check(
	ctx context.Context, d Domain,
) (bool, error) {
	var valid bool
	if s.scd != nil && d.ProviderUUID != "" {
		status, err := s.scd.VerifyDNS(ctx, d.ProviderUUID)
		if err != nil {
			return false, err
		}
		valid = status == "valid"
	} else {
		valid = s.dns.PointsAtUs(ctx, d.Host)
	}
	status := "pending"
	if valid {
		status = "active"
	}
	return valid, s.store.SetStatus(ctx, d.ID, status)
}

// Remove deletes from the proxy first: if that fails, the row is still
// here and the user can try again.
func (s *DomainService) Remove(ctx context.Context, d Domain) error {
	if s.scd != nil && d.ProviderUUID != "" {
		if err := s.scd.DeleteDomain(ctx, d.ProviderUUID); err != nil {
			return err
		}
	}
	return s.store.DeleteDomain(ctx, d.ID)
}

// DNSCheck is used when you run the proxy yourself. With SaaS Custom
// Domains, the API checks DNS for you.
type DNSCheck struct {
	CNAMETarget string   // what customers point a CNAME at
	ServerIPs   []string // or A records, for root domains
}

func (c DNSCheck) PointsAtUs(ctx context.Context, host string) bool {
	// *.localhost always resolves to this machine.
	if strings.HasSuffix(host, ".localhost") {
		return true
	}
	cname, err := net.DefaultResolver.LookupCNAME(ctx, host)
	if err == nil && normalizeHost(cname) == c.CNAMETarget {
		return true
	}
	ips, _ := net.DefaultResolver.LookupHost(ctx, host)
	return slices.ContainsFunc(ips, func(ip string) bool {
		return slices.Contains(c.ServerIPs, ip)
	})
}
