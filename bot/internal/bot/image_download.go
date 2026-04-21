package bot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	tele "gopkg.in/telebot.v3"
)

const maxVisionImageBytes = 3 * 1024 * 1024

func (s *Service) loadPhotoForModeration(ctx context.Context, msg *tele.Message) (string, string, error) {
	if msg == nil || msg.Photo == nil {
		return "", "", nil
	}

	media := msg.Photo.MediaFile()
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

func (s *Service) fetchTelegramFilePath(ctx context.Context, fileID string) (string, error) {
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/getFile?file_id=%s", s.cfg.BotToken, url.QueryEscape(fileID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("getFile status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
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
		return nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("file download status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxVisionImageBytes+1))
}
