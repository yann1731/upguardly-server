package monitor

import (
	"testing"
	"time"
)

func TestRegistrableDomain(t *testing.T) {
	cases := []struct {
		target  string
		want    string
		wantErr bool
	}{
		{"https://www.example.com/health", "example.com", false},
		{"http://sub.deep.example.co.uk", "example.co.uk", false},
		{"https://example.com:8443", "example.com", false},
		{"https://93.184.216.34", "", true}, // IP target: nothing to look up
		{"not a url", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			got, err := RegistrableDomain(tc.target)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tc.target)
				}
				return
			}
			if err != nil {
				t.Fatalf("RegistrableDomain(%q): %v", tc.target, err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseRDAPExpiry(t *testing.T) {
	t.Run("well-formed response with an expiration event", func(t *testing.T) {
		body := []byte(`{
			"objectClassName": "domain",
			"handle": "EXAMPLE.COM",
			"events": [
				{"eventAction": "registration", "eventDate": "2000-01-01T00:00:00Z"},
				{"eventAction": "expiration", "eventDate": "2027-01-01T00:00:00Z"},
				{"eventAction": "last changed", "eventDate": "2026-01-01T00:00:00Z"}
			]
		}`)
		got, err := ParseRDAPExpiry(body, "example.com")
		if err != nil {
			t.Fatalf("ParseRDAPExpiry: %v", err)
		}
		want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("no expiration event", func(t *testing.T) {
		body := []byte(`{"events": [{"eventAction": "registration", "eventDate": "2000-01-01T00:00:00Z"}]}`)
		if _, err := ParseRDAPExpiry(body, "example.com"); err == nil {
			t.Fatal("expected an error when no expiration event is present")
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		if _, err := ParseRDAPExpiry([]byte("not json"), "example.com"); err == nil {
			t.Fatal("expected a parse error for malformed JSON")
		}
	})

	t.Run("empty events list", func(t *testing.T) {
		if _, err := ParseRDAPExpiry([]byte(`{"events": []}`), "example.com"); err == nil {
			t.Fatal("expected an error for an empty events list")
		}
	})
}
