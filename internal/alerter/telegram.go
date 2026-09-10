package alerter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"

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

// telegramErrorResponse is the error envelope the Bot API returns alongside a
// non-2xx status. The description is the only thing that distinguishes the
// failure modes that matter operationally — "bot can't initiate conversation
// with a user" (the user never sent /start), "bot was blocked by the user",
// "chat not found" (wrong id), "not enough rights" (channel, bot not admin) —
// all of which arrive as a bare 403/400. Reporting the status alone makes
// every setup mistake look identical in AlertHistory.
type telegramErrorResponse struct {
	Description string `json:"description"`
	Parameters  struct {
		// RetryAfter is set on 429 and is the number of seconds the API wants
		// us to wait. Surfaced so the backoff is visible rather than guessed.
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// describeTelegramError turns a failed response into an error that names the
// cause. The body is read under a limit: it is attacker-influenced only via
// Telegram itself, but an unbounded ReadAll on an error path is how a hung
// upstream turns into memory pressure across every scheduler pool.
func describeTelegramError(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil || len(body) == 0 {
		return fmt.Errorf("telegram API returned status %d", resp.StatusCode)
	}

	var parsed telegramErrorResponse
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Description == "" {
		// Not the documented envelope (a proxy error page, say). Include a
		// short prefix for diagnosis — the whole string is persisted to
		// alert_history, so it must stay bounded.
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200] + "…"
		}
		return fmt.Errorf("telegram API returned status %d: %s", resp.StatusCode, snippet)
	}

	if parsed.Parameters.RetryAfter > 0 {
		return fmt.Errorf("telegram API returned status %d: %s (retry after %ds)",
			resp.StatusCode, parsed.Description, parsed.Parameters.RetryAfter)
	}
	return fmt.Errorf("telegram API returned status %d: %s", resp.StatusCode, parsed.Description)
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

	// Named endpoint, not url: it would otherwise shadow the net/url package
	// used below to strip this very string (it contains the token) out of
	// transport errors.
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", a.baseURL, a.cfg.BotToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// http.Client failures are *url.Error, whose Error() embeds the full
		// request URL — and for the Bot API the URL contains the token
		// (/bot<token>/sendMessage). This string is logged on every attempt and
		// persisted to alert_history on the last one, so unwrap to the
		// underlying cause rather than leaking the bot credential.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("failed to send telegram message: %w", urlErr.Err)
		}
		return fmt.Errorf("failed to send telegram message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return describeTelegramError(resp)
	}

	return nil
}
