package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeAPI answers like the SaaS Custom Domains API for one upstream.
func fakeAPI(t *testing.T, handler http.HandlerFunc) *SCDClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scd_token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return &SCDClient{BaseURL: srv.URL, Token: "scd_token", Account: "acc", Upstream: "up", HTTP: srv.Client()}
}

const domainsPath = "/api/v1/accounts/acc/upstreams/up/custom_domains"

func TestCreateDomain(t *testing.T) {
	client := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if r.Method != "POST" || r.URL.Path != domainsPath ||
			body["host"] != "go.acme.com" || body["instructions_recipient"] != "it@acme.com" {
			t.Errorf("unexpected request %s %s %v", r.Method, r.URL.Path, body)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"uuid":"dom_1","host":"go.acme.com","status":"pending_check"}`))
	})
	d, err := client.CreateDomain(context.Background(), "go.acme.com", "it@acme.com")
	if err != nil || d.UUID != "dom_1" {
		t.Fatalf("got %+v, %v", d, err)
	}
}

func TestAPIErrors(t *testing.T) {
	client := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Host has already been taken","error_code":"host_taken"}`))
	})
	_, err := client.CreateDomain(context.Background(), "go.acme.com", "")
	apiErr, ok := errors.AsType[*APIError](err)
	if !ok || apiErr.StatusCode != 400 || apiErr.Code != "host_taken" {
		t.Fatalf("got %v", err)
	}

	client.Token = "wrong"
	_, err = client.CreateDomain(context.Background(), "go.acme.com", "")
	if apiErr, ok := errors.AsType[*APIError](err); !ok || apiErr.StatusCode != 401 {
		t.Fatalf("got %v", err)
	}
}

func TestDeleteDomainTreats404AsDone(t *testing.T) {
	status := http.StatusNotFound
	client := fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(`{"error":"Custom domain dom_1 not found.","error_code":"not_found"}`))
	})
	if err := client.DeleteDomain(context.Background(), "dom_1"); err != nil {
		t.Errorf("404: %v", err)
	}
	status = http.StatusInternalServerError
	if err := client.DeleteDomain(context.Background(), "dom_1"); err == nil {
		t.Error("500: no error")
	}
}

func TestAddDomainKeepsNoRowWhenTheAPIFails(t *testing.T) {
	app := newTestApp(t)
	app.domains.scd = fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"Host has already been taken","error_code":"host_taken"}`))
	})

	w := serve(app, request("POST", "localhost", "/brands/1/domains host=go.taken.com"))
	if w.Code != 422 {
		t.Fatalf("got %d", w.Code)
	}
	if _, err := app.store.DomainByHost(context.Background(), "go.taken.com"); err != errNotFound {
		t.Errorf("row kept after API error: %v", err)
	}
}

func TestAddDomainStoresTheProviderUUID(t *testing.T) {
	app := newTestApp(t)
	app.domains.scd = fakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"uuid":"dom_new","host":"go.new.com","status":"pending_check"}`))
	})

	d, err := app.domains.Add(context.Background(), 1, "go.new.com", "")
	if err != nil || d.Status != "pending" {
		t.Fatalf("got %+v, %v", d, err)
	}
	stored, _ := app.store.DomainByHost(context.Background(), "go.new.com")
	if stored.ProviderUUID != "dom_new" {
		t.Errorf("provider_uuid = %q", stored.ProviderUUID)
	}
}
