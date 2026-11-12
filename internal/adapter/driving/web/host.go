package web

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// CanonicalHost returns the form of an authority (host, or host and port)
// that the Host check compares: the host in lower case, an IPv6 literal in
// brackets, and the port omitted when it is the default port of scheme
// ("http" or "https"). A final dot of a DNS name is dropped, since
// "raun.example." names the same host as "raun.example". It refuses an empty host, a port outside 1 to 65535
// and a scheme it does not know.
func CanonicalHost(authority, scheme string) (string, error) {
	var defaultPort string
	switch scheme {
	case "http":
		defaultPort = "80"
	case "https":
		defaultPort = "443"
	default:
		return "", fmt.Errorf("scheme %q is not http or https", scheme)
	}
	host, port := authority, ""
	if h, p, err := net.SplitHostPort(authority); err == nil {
		host, port = h, p
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port {
			return "", fmt.Errorf("port %q is not a number from 1 to 65535", port)
		}
	} else if strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]") {
		host = authority[1 : len(authority)-1]
	}
	if host == "" {
		return "", fmt.Errorf("empty host in %q", authority)
	}
	// Brackets hold an IPv6 literal and nothing else.
	if strings.HasPrefix(authority, "[") && (!strings.Contains(host, ":") || net.ParseIP(host) == nil) {
		return "", fmt.Errorf("host %q in brackets is not an IPv6 address", host)
	}
	if strings.ContainsAny(host, "[]/ ") || (strings.Contains(host, ":") && net.ParseIP(host) == nil) {
		return "", fmt.Errorf("host %q is not a name or an IP address", host)
	}
	if net.ParseIP(host) == nil {
		host = strings.TrimSuffix(host, ".")
		if host == "" || strings.HasSuffix(host, ".") {
			return "", fmt.Errorf("host %q is not a name or an IP address", authority)
		}
	}
	host = strings.ToLower(host)
	if port == "" || port == defaultPort {
		if strings.Contains(host, ":") {
			return "[" + host + "]", nil
		}
		return host, nil
	}
	return net.JoinHostPort(host, port), nil
}
