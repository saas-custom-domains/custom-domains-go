package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// SCDClient is a small client for the SaaS Custom Domains API.
// Docs: https://docs.saascustomdomains.com/custom-domains
type SCDClient struct {
	BaseURL  string // https://app.saascustomdomains.com
	Token    string
	Account  string
	Upstream string
	HTTP     *http.Client
}

type SCDDomain struct {
	UUID   string `json:"uuid"`
	Host   string `json:"host"`
	Status string `json:"status"`
}

// APIError is a non-2xx response. Validation errors are 400 with an
// error_code such as "host_taken".
type APIError struct {
	StatusCode int
	Message    string `json:"error"`
	Code       string `json:"error_code"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("saascustomdomains: %d %s: %s",
		e.StatusCode, e.Code, e.Message)
}

func (c *SCDClient) CreateDomain(
	ctx context.Context, host, dnsContact string,
) (SCDDomain, error) {
	body := map[string]string{"host": host}
	if dnsContact != "" {
		// We email this address the exact DNS records to add.
		body["instructions_recipient"] = dnsContact
	}
	var d SCDDomain
	err := c.do(ctx, http.MethodPost, "", body, &d)
	return d, err
}

// VerifyDNS checks DNS now. It returns "valid", "pending" or
// "invalid".
func (c *SCDClient) VerifyDNS(
	ctx context.Context, uuid string,
) (string, error) {
	var res struct {
		DNSStatus string `json:"dns_status"`
	}
	path := "/" + uuid + "/verify_dns_records"
	err := c.do(ctx, http.MethodPost, path, nil, &res)
	return res.DNSStatus, err
}

func (c *SCDClient) DeleteDomain(
	ctx context.Context, uuid string,
) error {
	err := c.do(ctx, http.MethodDelete, "/"+uuid, nil, nil)
	// Already gone is fine: the goal is that it doesn't exist.
	apiErr, ok := errors.AsType[*APIError](err)
	if ok && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

func (c *SCDClient) do(
	ctx context.Context, method, path string, body, out any,
) error {
	url := c.BaseURL + "/api/v1/accounts/" + c.Account +
		"/upstreams/" + c.Upstream + "/custom_domains" + path

	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		apiErr := &APIError{StatusCode: res.StatusCode}
		_ = json.NewDecoder(res.Body).Decode(apiErr) // 401 has no body
		return apiErr
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}
