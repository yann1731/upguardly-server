package alerter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"upguardly-backend/internal/models"
)

type DiscordAlerter struct {
	WebhookURL string
}

func NewDiscordAlerter() *DiscordAlerter {
	return &DiscordAlerter{}
}

type discordWebhookPayload struct {
	Content string         `json:"content,omitempty"`
	Embeds  []discordEmbed `json:"embeds,omitempty"`
}

type discordEmbed struct {
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Color       int          `json:"color"`
	Fields      []embedField `json:"fields,omitempty"`
}

type embedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline"`
}

func (a *DiscordAlerter) Send(ctx context.Context, monitor *models.Monitor, result *models.CheckResult) error {
	if a.WebhookURL == "" {
		return fmt.Errorf("discord webhook URL not configured")
	}

	color := getColorForStatus(result.Status)
	statusEmoji := getEmojiForStatus(result.Status)

	payload := discordWebhookPayload{
		Embeds: []discordEmbed{
			{
				Title:       fmt.Sprintf("%s Monitor Alert: %s", statusEmoji, monitor.Name),
				Description: fmt.Sprintf("Monitor **%s** is now **%s**", monitor.Name, result.Status),
				Color:       color,
				Fields: []embedField{
					{Name: "Type", Value: string(monitor.Type), Inline: true},
					{Name: "Target", Value: monitor.Target, Inline: true},
					{Name: "Latency", Value: fmt.Sprintf("%dms", result.Latency), Inline: true},
					{Name: "Message", Value: result.Message, Inline: false},
				},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal discord payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send discord webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("discord webhook returned status %d", resp.StatusCode)
	}

	return nil
}

func getColorForStatus(status models.Status) int {
	switch status {
	case models.StatusUP:
		return 0x00FF00 // Green
	case models.StatusDOWN:
		return 0xFF0000 // Red
	case models.StatusDEGRADED:
		return 0xFFA500 // Orange
	default:
		return 0x808080 // Gray
	}
}

func getEmojiForStatus(status models.Status) string {
	switch status {
	case models.StatusUP:
		return "✅"
	case models.StatusDOWN:
		return "🔴"
	case models.StatusDEGRADED:
		return "⚠️"
	default:
		return "❓"
	}
}
