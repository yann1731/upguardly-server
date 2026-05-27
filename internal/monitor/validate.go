package monitor

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"upguardly-backend/internal/models"
)

// privateRanges lists CIDR blocks that must never be targeted (SSRF prevention).
var privateRanges []*net.IPNet

func init() {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"127.0.0.0/8",        // IPv4 loopback
		"::1/128",            // IPv6 loopback
		"169.254.0.0/16",     // link-local / AWS & Azure metadata endpoint
		"fe80::/10",          // IPv6 link-local
		"fc00::/7",           // IPv6 unique local
		"0.0.0.0/8",          // "this" network
		"100.64.0.0/10",      // shared address space (RFC 6598)
		"192.0.0.0/24",       // IETF protocol assignments
		"198.18.0.0/15",      // benchmark testing
		"198.51.100.0/24",    // TEST-NET-2 (documentation)
		"203.0.113.0/24",     // TEST-NET-3 (documentation)
		"240.0.0.0/4",        // reserved / multicast
		"255.255.255.255/32", // IPv4 broadcast
	}
	for _, cidr := range cidrs {
		_, block, err := net.ParseCIDR(cidr)
		if err == nil {
			privateRanges = append(privateRanges, block)
		}
	}
}

func isPrivateIP(ip net.IP) bool {
	for _, block := range privateRanges {
		if block.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateTarget prevents SSRF by ensuring the monitor target is a publicly
// routable host. It resolves hostnames to IPs and rejects any address in a
// private / reserved range (loopback, RFC-1918, link-local, etc.).
//
// Validation is intentionally strict: when in doubt, reject. Legitimate
// external services should never resolve to a private IP.
func ValidateTarget(target string, monitorType models.MonitorType) error {
	var host string

	switch monitorType {
	case models.MonitorTypeHTTP:
		u, err := url.Parse(target)
		if err != nil || u.Host == "" {
			return fmt.Errorf("invalid URL: must be a valid absolute URL (e.g. https://example.com)")
		}
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("invalid URL scheme %q: only http and https are allowed", u.Scheme)
		}
		host = u.Hostname()

	case models.MonitorTypePORT:
		h, port, err := net.SplitHostPort(target)
		if err != nil || port == "" {
			return fmt.Errorf("invalid target: must be in host:port format (e.g. example.com:443)")
		}
		host = h

	case models.MonitorTypePING:
		host = target
		// Accept optional host:port form
		if h, _, err := net.SplitHostPort(target); err == nil {
			host = h
		}
		host = strings.TrimSpace(host)
		if host == "" {
			return fmt.Errorf("invalid target: host must not be empty")
		}

	default:
		return fmt.Errorf("unknown monitor type %q", monitorType)
	}

	if host == "" {
		return fmt.Errorf("invalid target: host must not be empty")
	}

	// If the target is a literal IP, check immediately without DNS resolution.
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("invalid target: private and reserved IP addresses are not allowed")
		}
		return nil
	}

	// Resolve the hostname and validate every returned address.
	// Resolving here prevents DNS rebinding attacks that use public IPs
	// initially but later switch to internal ones; the scheduler re-validates
	// at runtime so this adds a point-in-time check at creation.
	addrs, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("invalid target: hostname could not be resolved")
	}
	if len(addrs) == 0 {
		return fmt.Errorf("invalid target: hostname resolved to no addresses")
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("invalid target: hostname resolves to a private or reserved IP address")
		}
	}

	return nil
}
