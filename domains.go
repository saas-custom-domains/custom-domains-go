package main

import (
	"context"
	"errors"
)

// DomainService adds, checks and removes custom domains, keeping the
// database and SaaS Custom Domains in step. Whether a domain is live
// comes from the API: verify_dns_records here, webhooks otherwise.
type DomainService struct {
	store *Store
	scd   *SCDClient
}

func (s *DomainService) Add(
	ctx context.Context, brandID int64, host, dnsContact string,
) (Domain, error) {
	d, err := s.store.InsertDomain(ctx, brandID, host)
	if err != nil {
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
	status, err := s.scd.VerifyDNS(ctx, d.ProviderUUID)
	if err != nil {
		return false, err
	}
	valid := status == "valid"
	newStatus := "pending"
	if valid {
		newStatus = "active"
	}
	return valid, s.store.SetStatus(ctx, d.ID, newStatus)
}

// Remove deletes from the proxy first: if that fails, the row is still
// here and the user can try again.
func (s *DomainService) Remove(ctx context.Context, d Domain) error {
	if d.ProviderUUID != "" {
		err := s.scd.DeleteDomain(ctx, d.ProviderUUID)
		if err != nil {
			return err
		}
	}
	return s.store.DeleteDomain(ctx, d.ID)
}
