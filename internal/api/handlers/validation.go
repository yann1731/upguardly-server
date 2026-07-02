package handlers

import (
	"fmt"
	"net"
	"net/url"
	"regexp"

	"upguardly-backend/internal/models"
)

// e164Regexp validates E.164 international phone numbers.
var e164Regexp = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

// telegramChatIDRegexp validates Telegram targets: a numeric chat ID
// (negative for groups/supergroups) or a public @channelusername.
var telegramChatIDRegexp = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z][A-Za-z0-9_]{4,31})$`)

// validateAlertTarget checks that the alert destination is valid and safe.
// For webhook channels (DISCORD, SLACK) it also prevents SSRF by rejecting
// URLs that point to private or reserved IP ranges.
func validateAlertTarget(channel models.AlertChannel, target string) error {
	switch channel {
	case models.AlertChannelEMAIL:
		// The target is pinned server-side to the account email; nothing to
		// validate here.
		return nil

	case models.AlertChannelSMS:
		if !e164Regexp.MatchString(target) {
			return fmt.Errorf("invalid SMS target: must be an E.164 phone number (e.g. +12125551234)")
		}
		return nil

	case models.AlertChannelTELEGRAM:
		if !telegramChatIDRegexp.MatchString(target) {
			return fmt.Errorf("invalid Telegram target: must be a chat ID (e.g. 123456789) or @channelusername")
		}
		return nil

	case models.AlertChannelDISCORD, models.AlertChannelSLACK:
		u, err := url.Parse(target)
		if err != nil || u.Host == "" {
			return fmt.Errorf("invalid webhook URL: must be a valid absolute URL")
		}
		if u.Scheme != "https" {
			return fmt.Errorf("invalid webhook URL: only HTTPS webhooks are allowed")
		}
		// Extract the plain hostname (strip port if present).
		host := u.Hostname()
		// Block literal private IPs.
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
				return fmt.Errorf("invalid webhook URL: private IP addresses are not allowed")
			}
			return nil
		}
		// Resolve and validate every address the hostname maps to.
		addrs, err := net.LookupHost(host)
		if err != nil {
			return fmt.Errorf("invalid webhook URL: hostname could not be resolved")
		}
		for _, addr := range addrs {
			ip := net.ParseIP(addr)
			if ip == nil {
				continue
			}
			if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
				return fmt.Errorf("invalid webhook URL: hostname resolves to a private or reserved IP address")
			}
		}
		return nil
	}

	return nil
}

func isNotFound(err error) bool {
	return err == models.ErrNotFound
}
