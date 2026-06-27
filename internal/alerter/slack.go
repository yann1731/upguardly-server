package alerter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"upguardly-backend/internal/models"
)

type SlackAlerter struct{}

func NewSlackAlerter() *SlackAlerter {
	return &SlackAlerter{}
}

type slackWebhookPayload struct {
	Blocks []slackBlock `json:"blocks"`
}

type slackBlock struct {
	Type   string      `json:"type"`
	Text   *slackText  `json:"text,omitempty"`
	Fields []slackText `json:"fields,omitempty"`
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (a *SlackAlerter) Send(ctx context.Context, target string, monitor *models.Monitor, result *models.CheckResult) error {
	if target == "" {
		return fmt.Errorf("slack webhook URL not configured")
	}

	statusEmoji := getEmojiForStatus(result.Status)

	payload := slackWebhookPayload{
		Blocks: []slackBlock{
			{
				Type: "header",
				Text: &slackText{
					Type: "plain_text",
					Text: fmt.Sprintf("%s Monitor Alert: %s", statusEmoji, monitor.Name),
				},
			},
			{
				Type: "section",
				Text: &slackText{
					Type: "mrkdwn",
					Text: fmt.Sprintf("Monitor *%s* is now *%s*", monitor.Name, result.Status),
				},
			},
			{
				Type: "section",
				Fields: []slackText{
					{Type: "mrkdwn", Text: fmt.Sprintf("*Type:*\n%s", monitor.Type)},
					{Type: "mrkdwn", Text: fmt.Sprintf("*Target:*\n%s", monitor.Target)},
					{Type: "mrkdwn", Text: fmt.Sprintf("*Latency:*\n%dms", result.Latency)},
					{Type: "mrkdwn", Text: fmt.Sprintf("*Message:*\n%s", result.Message)},
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send slack webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("slack webhook returned status %d", resp.StatusCode)
	}

	return nil
}
