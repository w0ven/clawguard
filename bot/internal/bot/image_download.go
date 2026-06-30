package bot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"github.com/openclaw/clawguard/internal/redact"
)

const maxVisionImageBytes = 3 * 1024 * 1024

var imageDownloadHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		MaxConnsPerHost:       50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

func (s *Service) loadVisualForModeration(ctx context.Context, msg *tele.Message) (string, string, error) {
	media := visualMediaFile(msg)
	if media == nil || strings.TrimSpace(media.FileID) == "" {
		return "", "", nil
	}

	filePath, err := s.fetchTelegramFilePath(ctx, media.FileID)
	if err != nil {
		return "", "", err
	}
	body, err := s.downloadTelegramFile(ctx, filePath)
	if err != nil {
		return "", "", err
	}
	if len(body) == 0 || len(body) > maxVisionImageBytes {
		return "", "", nil
	}

	sum := sha256.Sum256(body)
	return base64.StdEncoding.EncodeToString(body), fmt.Sprintf("%x", sum[:]), nil
}

func visualMediaFile(msg *tele.Message) *tele.File {
	if msg == nil {
		return nil
	}
	if msg.Photo != nil {
		return msg.Photo.MediaFile()
	}
	if msg.Sticker == nil {
		return nil
	}
	if !msg.Sticker.Animated && !msg.Sticker.Video {
		return msg.Sticker.MediaFile()
	}
	if msg.Sticker.Thumbnail != nil {
		return msg.Sticker.Thumbnail.MediaFile()
	}
	return nil
}

func (s *Service) fetchTelegramFilePath(ctx context.Context, fileID string) (string, error) {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", s.cfg.BotToken, url.QueryEscape(fileID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", redact.Error(err)
	}

	resp, err := imageDownloadHTTPClient.Do(req)
	if err != nil {
		return "", redact.Error(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("getFile status %d: %s", resp.StatusCode, redact.Text(strings.TrimSpace(string(body))))
	}

	var payload struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", redact.Error(err)
	}
	if !payload.OK || strings.TrimSpace(payload.Result.FilePath) == "" {
		return "", fmt.Errorf("telegram getFile returned empty file_path")
	}
	return payload.Result.FilePath, nil
}

func (s *Service) downloadTelegramFile(ctx context.Context, filePath string) ([]byte, error) {
	endpoint := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", s.cfg.BotToken, strings.TrimLeft(filePath, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, redact.Error(err)
	}

	resp, err := imageDownloadHTTPClient.Do(req)
	if err != nil {
		return nil, redact.Error(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("file download status %d: %s", resp.StatusCode, redact.Text(strings.TrimSpace(string(body))))
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxVisionImageBytes+1))
}
