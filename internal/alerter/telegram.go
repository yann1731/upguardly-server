package alerter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"

	"upguardly-backend/internal/config"
	"upguardly-backend/internal/models"
)

type TelegramAlerter struct {
	cfg config.TelegramConfig
	// baseURL exists so tests can point Send at an httptest server; unlike
	// Discord/Slack the target is a chat ID, not the URL to post to.
	baseURL string
}

func NewTelegramAlerter(cfg config.TelegramConfig) *TelegramAlerter {
	return &TelegramAlerter{cfg: cfg, baseURL: "https://api.telegram.org"}
}

type telegramSendMessagePayload struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

func (a *TelegramAlerter) Send(ctx context.Context, target string, monitor *models.Monitor, result *models.CheckResult) error {
	if a.cfg.BotToken == "" {
		return fmt.Errorf("telegram bot token not configured")
	}
	if target == "" {
		return fmt.Errorf("telegram chat ID not configured")
	}

	statusEmoji := getEmojiForStatus(result.Status)

	// HTML parse mode with escaped interpolations: user-controlled fields
	// (name, target, message) must not be able to inject markup.
	text := fmt.Sprintf(
		"%s <b>Monitor Alert: %s</b>\nMonitor <b>%s</b> is now <b>%s</b>\n\n<b>Type:</b> %s\n<b>Target:</b> %s\n<b>Latency:</b> %dms\n<b>Message:</b> %s",
		statusEmoji,
		html.EscapeString(monitor.Name),
		html.EscapeString(monitor.Name),
		result.Status,
		monitor.Type,
		html.EscapeString(monitor.Target),
		result.Latency,
		html.EscapeString(result.Message),
	)

	payload := telegramSendMessagePayload{
		ChatID:    target,
		Text:      text,
		ParseMode: "HTML",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal telegram payload: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", a.baseURL, a.cfg.BotToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send telegram message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("telegram API returned status %d", resp.StatusCode)
	}

	return nil
}
