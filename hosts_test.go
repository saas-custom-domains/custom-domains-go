package main

import "testing"

func TestNormalizeHost(t *testing.T) {
	tests := map[string]string{
		" Go.Acme.com.:443 ":     "go.acme.com",
		"go.acme.localhost:3000": "go.acme.localhost",
		"go.acme.com":            "go.acme.com",
		"[::1]:3000":             "::1",
		"https://go.acme.com":    "https://go.acme.com", // not a host, so isValidHost rejects it
	}
	for in, want := range tests {
		if got := normalizeHost(in); got != want {
			t.Errorf("normalizeHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsValidHost(t *testing.T) {
	for _, host := range []string{"go.acme.com", "acme.co.uk", "xn--bcher-kva.example"} {
		if !isValidHost(host) {
			t.Errorf("isValidHost(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"acme", "-acme.com", "acme-.com", "acme..com", "ac me.com", "https://a.com", ""} {
		if isValidHost(host) {
			t.Errorf("isValidHost(%q) = true, want false", host)
		}
	}
}

func TestIsReserved(t *testing.T) {
	h := Hosts{App: []string{"localhost", "app.snip.example"}, Domain: "snip.example"}
	for host, want := range map[string]bool{
		"localhost":          true,
		"snip.example":       true,
		"admin.snip.example": true,
		"notsnip.example":    false,
		"go.acme.com":        false,
	} {
		if got := h.IsReserved(host); got != want {
			t.Errorf("IsReserved(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestSecureCompare(t *testing.T) {
	tests := []struct {
		given, expected string
		want            bool
	}{
		{"abc", "abc", true},
		{"abd", "abc", false},
		{"ab", "abc", false},
		{"", "", false}, // an unset token must not let empty headers through
	}
	for _, tt := range tests {
		if got := secureCompare(tt.given, tt.expected); got != tt.want {
			t.Errorf("secureCompare(%q, %q) = %v, want %v", tt.given, tt.expected, got, tt.want)
		}
	}
}
