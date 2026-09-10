package alerter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"upguardly-backend/internal/config"
)

func testTwilioConfig() config.TwilioConfig {
	return config.TwilioConfig{
		AccountSID:   "AC123",
		APIKeySID:    "SK456",
		APIKeySecret: "secret",
		FromNumber:   "+15550000000",
	}
}

func TestSMSSend(t *testing.T) {
	var gotPath, gotTo, gotFrom, gotBody, gotUser, gotPass string
	var gotAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotUser, gotPass, gotAuth = r.BasicAuth()
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		gotTo = r.PostForm.Get("To")
		gotFrom = r.PostForm.Get("From")
		gotBody = r.PostForm.Get("Body")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	a := NewSMSAlerter(testTwilioConfig())
	a.baseURL = srv.URL

	monitor, result := testMonitorAndResult()
	if err := a.Send(context.Background(), "+15551234567", monitor, result); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if gotPath != "/2010-04-01/Accounts/AC123/Messages.json" {
		t.Errorf("path = %q, want /2010-04-01/Accounts/AC123/Messages.json", gotPath)
	}
	if !gotAuth || gotUser != "SK456" || gotPass != "secret" {
		t.Errorf("basic auth = (%q, %q, %v), want (SK456, secret, true)", gotUser, gotPass, gotAuth)
	}
	if gotTo != "+15551234567" || gotFrom != "+15550000000" {
		t.Errorf("To/From = %q/%q", gotTo, gotFrom)
	}
	if !strings.Contains(gotBody, "DOWN") || !strings.Contains(gotBody, monitor.Name) {
		t.Errorf("body missing status/name: %q", gotBody)
	}
}

func TestSMSSendMissingConfig(t *testing.T) {
	a := NewSMSAlerter(config.TwilioConfig{})
	monitor, result := testMonitorAndResult()
	if err := a.Send(context.Background(), "+15551234567", monitor, result); err == nil {
		t.Fatal("Send with no Twilio config: want error, got nil")
	}
}

func TestSMSSendMissingTarget(t *testing.T) {
	a := NewSMSAlerter(testTwilioConfig())
	monitor, result := testMonitorAndResult()
	if err := a.Send(context.Background(), "", monitor, result); err == nil {
		t.Fatal("Send with no target: want error, got nil")
	}
}

// TestSMSSendAPIErrorDescription checks that Twilio's message and error code
// survive into the returned error as a sentence rather than Go map syntax.
func TestSMSSendAPIErrorDescription(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    []string
		notWant []string
	}{
		{
			name:   "invalid recipient",
			status: http.StatusBadRequest,
			body:   `{"code":21211,"message":"The 'To' number +1555 is not a valid phone number.","more_info":"https://www.twilio.com/docs/errors/21211","status":400}`,
			want:   []string{"400", "not a valid phone number", "code 21211", "https://www.twilio.com/docs/errors/21211"},
		},
		{
			name:   "authentication failure",
			status: http.StatusUnauthorized,
			body:   `{"code":20003,"message":"Authenticate","more_info":"https://www.twilio.com/docs/errors/20003","status":401}`,
			want:   []string{"401", "Authenticate", "code 20003"},
		},
		{
			name:   "rate limited",
			status: http.StatusTooManyRequests,
			body:   `{"code":20429,"message":"Too Many Requests","status":429}`,
			want:   []string{"429", "Too Many Requests", "code 20429"},
		},
		{
			name:    "non-envelope body is truncated but reported",
			status:  http.StatusBadGateway,
			body:    "<html>upstream proxy failure</html>",
			want:    []string{"502", "upstream proxy failure"},
			notWant: []string{"map["},
		},
		{
			name:    "empty body reports status only",
			status:  http.StatusServiceUnavailable,
			body:    "",
			want:    []string{"Twilio returned status 503"},
			notWant: []string{"map[", ":"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			a := NewSMSAlerter(testTwilioConfig())
			a.baseURL = srv.URL

			monitor, result := testMonitorAndResult()
			err := a.Send(context.Background(), "+15551234567", monitor, result)
			if err == nil {
				t.Fatalf("Send with %d response: want error, got nil", tt.status)
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error = %q, want it to contain %q", err, want)
				}
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(err.Error(), notWant) {
					t.Errorf("error = %q, want it not to contain %q", err, notWant)
				}
			}
		})
	}
}

// TestSMSSendErrorBodyBounded keeps an oversized upstream body out of
// alert_history, both for the raw fallback and for an oversized envelope.
func TestSMSSendErrorBodyBounded(t *testing.T) {
	bodies := map[string]string{
		"raw":      strings.Repeat("x", 64<<10),
		"envelope": `{"code":21211,"message":"` + strings.Repeat("y", 6<<10) + `"}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(body))
			}))
			defer srv.Close()

			a := NewSMSAlerter(testTwilioConfig())
			a.baseURL = srv.URL

			monitor, result := testMonitorAndResult()
			err := a.Send(context.Background(), "+15551234567", monitor, result)
			if err == nil {
				t.Fatal("Send with oversized body: want error, got nil")
			}
			if len(err.Error()) > 512 {
				t.Errorf("error is %d bytes, want it bounded: %.128q…", len(err.Error()), err)
			}
		})
	}
}
