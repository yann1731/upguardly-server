package alerter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"upguardly-backend/internal/config"
	"upguardly-backend/internal/models"
)

type SMSAlerter struct {
	config config.TwilioConfig
	// baseURL exists so tests can point Send at an httptest server, matching
	// TelegramAlerter. Production always uses the real API host.
	baseURL string
}

func NewSMSAlerter(cfg config.TwilioConfig) *SMSAlerter {
	return &SMSAlerter{
		config:  cfg,
		baseURL: "https://api.twilio.com",
	}
}

func (a *SMSAlerter) Send(ctx context.Context, target string, monitor *models.Monitor, result *models.CheckResult) error {
	if a.config.AccountSID == "" || a.config.APIKeySID == "" || a.config.APIKeySecret == "" {
		return fmt.Errorf("Twilio not configured")
	}

	if target == "" {
		return fmt.Errorf("recipient phone number not set")
	}

	statusEmoji := ""
	switch result.Status {
	case models.StatusUP:
		statusEmoji = "✅"
	case models.StatusDOWN:
		statusEmoji = "🔴"
	case models.StatusDEGRADED:
		statusEmoji = "⚠️"
	}

	message := fmt.Sprintf("%s Upguardly: %s is %s\nTarget: %s\nLatency: %s",
		statusEmoji, monitor.Name, result.Status, monitor.Target, formatLatency(result))

	twilioURL := fmt.Sprintf("%s/2010-04-01/Accounts/%s/Messages.json", a.baseURL, a.config.AccountSID)

	data := url.Values{}
	data.Set("To", target)
	data.Set("From", a.config.FromNumber)
	data.Set("Body", message)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, twilioURL, strings.NewReader(data.Encode()))
	if err != nil {
		// url.Parse failures are *url.Error and embed the URL.
		return sanitizeTransportError("failed to create request", err)
	}

	// API key auth: the URL path is scoped to the Account SID, while the
	// request is authenticated with the API key SID + secret.
	req.SetBasicAuth(a.config.APIKeySID, a.config.APIKeySecret)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Consistent with the other alerters. Twilio authenticates with a
		// Basic-auth header, so this URL carries only the Account SID — an
		// identifier, not a credential — but keeping every alerter on one
		// path means the next one added inherits the safe default.
		return sanitizeTransportError("failed to send SMS", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var errResp map[string]interface{}
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("Twilio returned status %d: %v", resp.StatusCode, errResp)
	}

	return nil
}

// Unused but kept for potential future JSON body approach
func marshalJSON(v interface{}) *bytes.Buffer {
	buf := &bytes.Buffer{}
	json.NewEncoder(buf).Encode(v)
	return buf
}
