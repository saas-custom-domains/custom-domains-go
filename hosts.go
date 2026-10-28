package main

import (
	"net"
	"regexp"
	"slices"
	"strings"
)

// normalizeHost turns " Go.Acme.com.:443 " into "go.acme.com".
// r.Host keeps whatever port the client used ("go.acme.localhost:3000"),
// so every lookup has to go through this first.
func normalizeHost(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if host, port, err := net.SplitHostPort(s); err == nil && isDigits(port) {
		s = host
	}
	return strings.TrimSuffix(s, ".")
}

var label = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// isValidHost accepts hostnames with at least two labels, like
// "go.acme.com". It expects a normalized host.
func isValidHost(host string) bool {
	if len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !label.MatchString(l) {
			return false
		}
	}
	return true
}

// Hosts knows which hostnames belong to this app rather than to a customer.
type Hosts struct {
	// App lists the hostnames that serve the dashboard, e.g.
	// "snip.example" plus any internal name your platform uses.
	App []string
	// Domain is the domain customers can't claim, with its subdomains.
	Domain string
}

func (h Hosts) IsApp(host string) bool {
	return slices.Contains(h.App, host)
}

func (h Hosts) IsReserved(host string) bool {
	if h.IsApp(host) {
		return true
	}
	return h.Domain != "" && (host == h.Domain || strings.HasSuffix(host, "."+h.Domain))
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}
