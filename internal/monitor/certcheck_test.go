package monitor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckCertExpiry(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()

	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())

	// A plain dialer bypasses SafeDialer's SSRF guard (that guard rightly
	// blocks 127.0.0.1 and has its own tests); the RootCAs override trusts
	// the httptest server's ephemeral self-signed CA instead of skipping
	// verification. The generated cert's validity window is fixed far in the
	// future relative to any real test run; we only assert a plausible
	// NotAfter, not an exact date.
	expiry, err := checkCertExpiry(context.Background(), srv.URL, 5*time.Second, &net.Dialer{}, &tls.Config{RootCAs: pool})
	if err != nil {
		t.Fatalf("CheckCertExpiry: %v", err)
	}
	if !expiry.After(time.Now()) {
		t.Fatalf("expected a future expiry, got %v", expiry)
	}
}

func TestCheckCertExpiry_InvalidTarget(t *testing.T) {
	if _, err := CheckCertExpiry(context.Background(), "not a url", time.Second); err == nil {
		t.Fatal("expected an error for an invalid target URL")
	}
}

func TestCheckCertExpiry_ConnectionRefused(t *testing.T) {
	// Port 1 is reserved and nothing should be listening there.
	if _, err := checkCertExpiry(context.Background(), "https://localhost:1", time.Second, &net.Dialer{}, nil); err == nil {
		t.Fatal("expected a connect error")
	}
}

func TestCheckCertExpiry_SSRFGuardBlocksPrivateIP(t *testing.T) {
	// The public entry point goes through SafeDialer, which must reject a
	// private-IP target regardless of what's listening there.
	if _, err := CheckCertExpiry(context.Background(), "https://127.0.0.1:1", time.Second); err == nil {
		t.Fatal("expected the SSRF guard to block a private IP target")
	}
}
