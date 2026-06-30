package bot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/openclaw/clawguard/internal/config"
	"github.com/openclaw/clawguard/internal/redact"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"
	tele "gopkg.in/telebot.v3"
)

type VideoFetchResult struct {
	Frames    [][]byte
	Meta      videoMeta
	Truncated bool
}

var (
	videoSemOnce sync.Once
	videoSem     *semaphore.Weighted

	ffmpegPathOnce sync.Once
	ffmpegPath     string
	ffprobePath    string
)

func (s *Service) fetchVideoFrames(ctx context.Context, msg *tele.Message, declared videoMeta, cfg config.AIPolicy) (VideoFetchResult, error) {
	result := VideoFetchResult{Meta: declared}
	initFFmpegPaths()
	if ffmpegPath == "" {
		return result, errors.New("ffmpeg not found")
	}

	concurrency := cfg.VideoConcurrency
	if concurrency <= 0 {
		concurrency = config.DefaultPolicy.AI.VideoConcurrency
	}
	videoSemOnce.Do(func() {
		videoSem = semaphore.NewWeighted(int64(concurrency))
	})
	if videoSem == nil || !videoSem.TryAcquire(1) {
		result.Truncated = true
		return result, nil
	}
	defer videoSem.Release(1)

	maxBytes := cfg.VideoMaxBytes
	if maxBytes <= 0 {
		maxBytes = config.DefaultPolicy.AI.VideoMaxBytes
	}
	if declared.ByteSize > maxBytes {
		result.Truncated = true
		return result, nil
	}

	maxDuration := cfg.VideoMaxDurationSec
	if maxDuration <= 0 {
		maxDuration = config.DefaultPolicy.AI.VideoMaxDurationSec
	}
	if declared.DurationSec > maxDuration {
		result.Truncated = true
		return result, nil
	}

	fileID := strings.TrimSpace(declared.FileID)
	if fileID == "" && msg != nil {
		fileID = videoFileIDFromMessage(msg)
	}
	if fileID == "" {
		return result, errors.New("empty video file id")
	}

	filePath, err := s.fetchTelegramFilePath(ctx, fileID)
	if err != nil {
		return result, err
	}
	body, err := downloadTelegramFileWithCap(ctx, s.cfg.BotToken, filePath, maxBytes+1)
	if err != nil {
		return result, err
	}
	if int64(len(body)) > maxBytes {
		result.Truncated = true
		return result, nil
	}

	tmpdir, err := os.MkdirTemp("", "clawguard-video-*")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(tmpdir)

	inputPath := filepath.Join(tmpdir, "input")
	if err := os.WriteFile(inputPath, body, 0600); err != nil {
		return result, err
	}

	duration := declared.DurationSec
	if probed, err := probeVideoDuration(ctx, inputPath); err != nil {
		s.logger.Debug("probe video duration failed", zap.Error(err), zap.Int64("chat_id", chatIDFromMessage(msg)))
	} else if probed > 0 {
		duration = probed
		result.Meta.DurationSec = probed
	}
	if duration <= 0 {
		duration = 1
		result.Meta.DurationSec = 1
	}
	if duration > maxDuration {
		result.Truncated = true
		return result, nil
	}

	frameCount := cfg.VideoFrameCount
	if frameCount <= 0 {
		frameCount = config.DefaultPolicy.AI.VideoFrameCount
	}
	if declared.Source == "animation" {
		frameCount = 1
	}
	frames, err := extractKeyFramesFFmpeg(ctx, inputPath, duration, frameCount)
	if err != nil {
		return result, err
	}
	result.Frames = frames
	return result, nil
}

func extractKeyFramesFFmpeg(ctx context.Context, inputPath string, durationSec int, n int) ([][]byte, error) {
	initFFmpegPaths()
	if ffmpegPath == "" {
		return nil, errors.New("ffmpeg not found")
	}
	if durationSec <= 0 {
		durationSec = 1
	}
	if n < 1 {
		n = 1
	}
	if n > 3 {
		n = 3
	}

	duration := float64(durationSec)
	t0 := 0.5
	tMid := duration / 2
	tEnd := maxFloat(duration-1, tMid+0.5)
	if tEnd < 0 {
		tEnd = 0
	}

	times := []float64{tMid}
	switch n {
	case 2:
		times = []float64{t0, tEnd}
	case 3:
		times = []float64{t0, tMid, tEnd}
	}

	tmpdir, err := os.MkdirTemp("", "clawguard-frames-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpdir)

	frames := make([][]byte, 0, len(times))
	for idx, at := range times {
		outputPath := filepath.Join(tmpdir, fmt.Sprintf("frame_%d.jpg", idx))
		cmd := exec.CommandContext(ctx, ffmpegPath, "-ss", strconv.FormatFloat(at, 'f', 3, 64), "-i", inputPath, "-frames:v", "1", "-vf", "scale=512:-2", "-q:v", "4", "-y", outputPath)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("ffmpeg extract frame failed: %w: %s", err, tailString(out, 200))
		}
		body, err := os.ReadFile(outputPath)
		if err != nil {
			return nil, err
		}
		if len(body) > 0 {
			frames = append(frames, body)
		}
	}
	return frames, nil
}

func probeVideoDuration(ctx context.Context, inputPath string) (int, error) {
	initFFmpegPaths()
	if ffprobePath == "" {
		return 0, errors.New("ffprobe not found")
	}
	cmd := exec.CommandContext(ctx, ffprobePath, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", inputPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("ffprobe duration failed: %w: %s", err, tailString(out, 200))
	}
	value := strings.TrimSpace(string(out))
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, err
	}
	if seconds <= 0 {
		return 0, nil
	}
	return int(seconds + 0.5), nil
}

func downloadTelegramFileWithCap(ctx context.Context, botToken, filePath string, capBytes int64) ([]byte, error) {
	if capBytes <= 0 {
		return nil, errors.New("invalid cap bytes")
	}
	endpoint := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", botToken, strings.TrimLeft(filePath, "/"))
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
	return io.ReadAll(io.LimitReader(resp.Body, capBytes))
}

func initFFmpegPaths() {
	ffmpegPathOnce.Do(func() {
		ffmpegPath, _ = exec.LookPath("ffmpeg")
		ffprobePath, _ = exec.LookPath("ffprobe")
	})
}

func videoFileIDFromMessage(msg *tele.Message) string {
	if msg == nil {
		return ""
	}
	if msg.Video != nil {
		return strings.TrimSpace(msg.Video.FileID)
	}
	if msg.Animation != nil {
		return strings.TrimSpace(msg.Animation.FileID)
	}
	if msg.VideoNote != nil {
		return strings.TrimSpace(msg.VideoNote.FileID)
	}
	return ""
}

func chatIDFromMessage(msg *tele.Message) int64 {
	if msg == nil || msg.Chat == nil {
		return 0
	}
	return msg.Chat.ID
}

func tailString(body []byte, max int) string {
	body = bytes.TrimSpace(body)
	if len(body) <= max {
		return string(body)
	}
	return string(body[len(body)-max:])
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}
