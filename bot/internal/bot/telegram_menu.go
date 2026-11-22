package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const telegramMenuResponseLimit = 8 << 10

var telegramMenuHTTPClient = &http.Client{Timeout: 10 * time.Second}

type telegramMenuResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

func setDefaultMiniAppMenuButton(ctx context.Context, botToken, text, miniAppURL string) error {
	endpoint := "https://api.telegram.org/bot" + botToken + "/setChatMenuButton"
	return postMiniAppMenuButton(ctx, telegramMenuHTTPClient, endpoint, 0, text, miniAppURL)
}

func setChatMiniAppMenuButton(ctx context.Context, botToken string, chatID int64, text, miniAppURL string) error {
	endpoint := "https://api.telegram.org/bot" + botToken + "/setChatMenuButton"
	return postMiniAppMenuButton(ctx, telegramMenuHTTPClient, endpoint, chatID, text, miniAppURL)
}

func postDefaultMiniAppMenuButton(ctx context.Context, client *http.Client, endpoint, miniAppURL string) error {
	return postMiniAppMenuButton(ctx, client, endpoint, 0, "管理面板", miniAppURL)
}

func postMiniAppMenuButton(ctx context.Context, client *http.Client, endpoint string, chatID int64, text, miniAppURL string) error {
	menuButton, err := json.Marshal(map[string]any{
		"type":    "web_app",
		"text":    text,
		"web_app": map[string]string{"url": miniAppURL},
	})
	if err != nil {
		return fmt.Errorf("encode menu button: %w", err)
	}

	form := url.Values{"menu_button": {string(menuButton)}}
	if chatID != 0 {
		form.Set("chat_id", strconv.FormatInt(chatID, 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create Telegram menu request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		// Transport errors can contain the endpoint, which embeds the bot token.
		return fmt.Errorf("Telegram menu request failed (%T)", err)
	}
	defer resp.Body.Close()

	var result telegramMenuResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, telegramMenuResponseLimit)).Decode(&result); err != nil {
		return fmt.Errorf("decode Telegram menu response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || !result.OK {
		description := strings.TrimSpace(result.Description)
		if description == "" {
			description = "request rejected"
		}
		return fmt.Errorf("Telegram menu request rejected (status %d): %s", resp.StatusCode, description)
	}
	return nil
}
