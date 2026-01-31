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
	To     string
}

func NewSMSAlerter(cfg config.TwilioConfig) *SMSAlerter {
	return &SMSAlerter{
		config: cfg,
	}
}

func (a *SMSAlerter) Send(ctx context.Context, monitor *models.Monitor, result *models.CheckResult) error {
	if a.config.AccountSID == "" || a.config.AuthToken == "" {
		return fmt.Errorf("Twilio not configured")
	}

	if a.To == "" {
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

	message := fmt.Sprintf("%s Upguardly: %s is %s\nTarget: %s\nLatency: %dms",
		statusEmoji, monitor.Name, result.Status, monitor.Target, result.Latency)

	twilioURL := fmt.Sprintf("https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json", a.config.AccountSID)

	data := url.Values{}
	data.Set("To", a.To)
	data.Set("From", a.config.FromNumber)
	data.Set("Body", message)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, twilioURL, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(a.config.AccountSID, a.config.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send SMS: %w", err)
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
