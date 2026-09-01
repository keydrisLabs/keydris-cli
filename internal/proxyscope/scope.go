// Package proxyscope classifies destinations that Keydris should govern.
package proxyscope

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const (
	ModeAll      = "all"
	ModeSelected = "selected"
)

// Scope matches exact, canonical host:port destinations.
type Scope struct {
	mode         string
	destinations map[string]struct{}
}

// New validates and canonicalizes a managed-destination configuration.
func New(mode string, destinations []string) (*Scope, error) {
	if mode == "" {
		mode = ModeAll
	}
	if mode != ModeAll && mode != ModeSelected {
		return nil, fmt.Errorf("unknown proxy scope mode %q (want all or selected)", mode)
	}
	s := &Scope{mode: mode, destinations: make(map[string]struct{}, len(destinations))}
	for _, raw := range destinations {
		dst, err := Normalize(raw)
		if err != nil {
			return nil, err
		}
		s.destinations[dst] = struct{}{}
	}
	return s, nil
}

// Managed reports whether dst should be authorized and credential-injected.
func (s *Scope) Managed(dst string) bool {
	if s == nil || s.mode == ModeAll {
		return true
	}
	canonical, err := Normalize(dst)
	if err != nil {
		return false
	}
	_, ok := s.destinations[canonical]
	return ok
}

func (s *Scope) Mode() string {
	if s == nil || s.mode == "" {
		return ModeAll
	}
	return s.mode
}

func (s *Scope) Destinations() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.destinations))
	for dst := range s.destinations {
		out = append(out, dst)
	}
	sort.Strings(out)
	return out
}

// Normalize returns a canonical exact host:port. Inputs may be URLs,
// host:port pairs, hostnames (default :443), or IP literals.
func Normalize(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("managed destination is empty")
	}

	defaultPort := "443"
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "", fmt.Errorf("invalid managed destination %q", raw)
		}
		if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("managed destination %q contains a path; only origins are supported", raw)
		}
		raw = u.Host
		if u.Scheme == "http" {
			defaultPort = "80"
		} else if u.Scheme != "https" {
			return "", fmt.Errorf("managed destination %q uses unsupported scheme %q", raw, u.Scheme)
		}
	}

	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		if strings.Contains(raw, ":") {
			if ip := net.ParseIP(strings.Trim(raw, "[]")); ip != nil {
				host = ip.String()
				port = defaultPort
			} else {
				return "", fmt.Errorf("invalid managed destination %q: include an explicit port", raw)
			}
		} else {
			host = raw
			port = defaultPort
		}
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if host == "" || port == "" {
		return "", fmt.Errorf("invalid managed destination %q", raw)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", fmt.Errorf("invalid managed destination %q: port must be between 1 and 65535", raw)
	}
	port = strconv.Itoa(portNumber)
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	return net.JoinHostPort(host, port), nil
}
