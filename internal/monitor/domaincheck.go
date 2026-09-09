package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/publicsuffix"
)

// rdapEndpoint is the RDAP bootstrap redirector: it forwards to the
// registry's RDAP server for any TLD that has one. RDAP only (no WHOIS
// fallback) — WHOIS response formats are a parsing zoo, RDAP is structured
// JSON. Some ccTLDs lack RDAP; those record last_error and never alert.
const rdapEndpoint = "https://rdap.org/domain/"

// rdapDomainResponse is the subset of an RDAP domain object we care about.
type rdapDomainResponse struct {
	Events []struct {
		EventAction string    `json:"eventAction"`
		EventDate   time.Time `json:"eventDate"`
	} `json:"events"`
}

// RegistrableDomain extracts the registrable domain (eTLD+1) from an HTTP
// monitor's target URL — the unit WHOIS/RDAP expiry applies to (the registered
// name, not the subdomain). IP-literal targets have no domain to check.
func RegistrableDomain(target string) (string, error) {
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid target URL")
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil {
		return "", fmt.Errorf("domain expiry requires a hostname target, not an IP address")
	}
	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", fmt.Errorf("no registrable domain in %q: %w", host, err)
	}
	return domain, nil
}

// CheckDomainExpiry looks up the registrable domain's expiration date via
// RDAP. One retry on failure; errors are recorded by the caller as last_error
// and back off naturally (checked_at advances at claim time, so a failing
// domain is retried daily, never hot-looped).
func CheckDomainExpiry(ctx context.Context, target string, timeout time.Duration) (time.Time, error) {
	domain, err := RegistrableDomain(target)
	if err != nil {
		return time.Time{}, err
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		expiry, err := fetchRDAPExpiry(ctx, domain, timeout)
		if err == nil {
			return expiry, nil
		}
		lastErr = err
	}
	return time.Time{}, lastErr
}

func fetchRDAPExpiry(ctx context.Context, domain string, timeout time.Duration) (time.Time, error) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rdapEndpoint+url.PathEscape(domain), nil)
	if err != nil {
		return time.Time{}, err
	}
	req.Header.Set("Accept", "application/rdap+json")
	req.Header.Set("User-Agent", "Upguardly-Monitor/1.0")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("RDAP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return time.Time{}, fmt.Errorf("RDAP lookup for %s returned HTTP %d", domain, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return time.Time{}, fmt.Errorf("RDAP response read: %w", err)
	}
	return ParseRDAPExpiry(body, domain)
}

// ParseRDAPExpiry extracts the "expiration" event date from an RDAP domain
// response body. Split out for fixture-based tests (CI never hits rdap.org).
func ParseRDAPExpiry(body []byte, domain string) (time.Time, error) {
	var parsed rdapDomainResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return time.Time{}, fmt.Errorf("RDAP response parse: %w", err)
	}
	for _, ev := range parsed.Events {
		if ev.EventAction == "expiration" && !ev.EventDate.IsZero() {
			return ev.EventDate, nil
		}
	}
	return time.Time{}, fmt.Errorf("RDAP response for %s carries no expiration event", domain)
}
