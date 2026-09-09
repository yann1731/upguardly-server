package monitor

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"time"
)

// CheckCertExpiry connects to the HTTPS endpoint behind an HTTP monitor's
// target URL and returns the leaf certificate's NotAfter. The dial goes
// through SafeDialer so the SSRF guard applies here too. Verification is NOT
// skipped: a handshake failure is returned as an error for the caller to
// record as last_error (no expiry alert fires on a broken chain — the regular
// HTTP check already reports "TLS certificate error" for that).
func CheckCertExpiry(ctx context.Context, target string, timeout time.Duration) (time.Time, error) {
	return checkCertExpiry(ctx, target, timeout, SafeDialer(), nil)
}

// checkCertExpiry does the real work with an injectable dialer and an
// optional *tls.Config override (nil = default: verify against the system
// root pool). Tests use both knobs to bypass the SSRF guard and trust a local
// httptest server's ephemeral CA — the guard itself is validate_test.go's job.
func checkCertExpiry(ctx context.Context, target string, timeout time.Duration, dialer *net.Dialer, tlsConfig *tls.Config) (time.Time, error) {
	u, err := url.Parse(target)
	if err != nil || u.Hostname() == "" {
		return time.Time{}, fmt.Errorf("invalid target URL")
	}
	host := u.Hostname()
	// The TLS endpoint is port 443 unless the URL pins an explicit port on an
	// https scheme; an http:// URL's port (usually 80) is not a TLS port.
	port := "443"
	if u.Scheme == "https" && u.Port() != "" {
		port = u.Port()
	}

	dialer.Timeout = timeout

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	rawConn, err := dialer.DialContext(dialCtx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return time.Time{}, fmt.Errorf("connect: %w", err)
	}
	cfg := tlsConfig.Clone()
	if cfg == nil {
		cfg = &tls.Config{}
	}
	cfg.ServerName = host
	conn := tls.Client(rawConn, cfg)
	defer conn.Close()

	if err := conn.HandshakeContext(dialCtx); err != nil {
		return time.Time{}, fmt.Errorf("TLS handshake: %w", err)
	}

	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return time.Time{}, fmt.Errorf("no peer certificate presented")
	}
	return certs[0].NotAfter, nil
}
