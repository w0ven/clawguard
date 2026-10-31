package scheduler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/openclaw/clawguard/internal/redact"
	"github.com/robfig/cron/v3"
	"go.uber.org/zap"
	tele "gopkg.in/telebot.v3"
)

// cron invokes Next when it creates each activation, before starting the job.
// Preserve those exact activation times instead of guessing from wall time
// after a delayed goroutine starts.
type slottedSchedule struct {
	cron.Schedule
	mu    sync.Mutex
	slots []time.Time
}

func (s *slottedSchedule) Next(t time.Time) time.Time {
	next := s.Schedule.Next(t)
	if !next.IsZero() {
		s.mu.Lock()
		s.slots = append(s.slots, next.UTC())
		s.mu.Unlock()
	}
	return next
}
func (s *slottedSchedule) take() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.slots) == 0 {
		return time.Time{}, false
	}
	t := s.slots[0]
	s.slots = s.slots[1:]
	return t, true
}
func (s *Scheduler) scheduleSlot(schedule cron.Schedule, id int64) cron.EntryID {
	item := &slottedSchedule{Schedule: schedule}
	return s.cron.Schedule(item, cron.FuncJob(func() {
		slot, ok := item.take()
		if ok {
			if err := s.runSlot(s.ctx, id, false, &slot); err != nil {
				s.logger.Warn("scheduled dispatch failed", zap.Int64("scheduled_message_id", id), zap.Time("scheduled_at", slot), zap.Error(err))
			}
		}
	}))
}

func (s *Scheduler) deleteWorker(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		job, err := s.claimDelete(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Warn("claim scheduled deletion", zap.Error(err))
			}
		} else if job {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Scheduler) claimDelete(ctx context.Context) (bool, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return false, err
	}
	job, err := s.queries.ClaimScheduledDelete(ctx, hex.EncodeToString(bytes[:]))
	if err != nil || job == nil {
		return false, err
	}
	if ctx.Err() != nil {
		// No network request has begun: release the lease without consuming
		// retry budget. If storage is unavailable, lease expiry still recovers.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		if releaseErr := s.queries.ReleaseScheduledDeleteBeforeCall(releaseCtx, *job); releaseErr != nil {
			s.logger.Warn("release canceled scheduled deletion", zap.Int64("delete_job_id", job.ID), zap.Error(releaseErr))
		}
		cancel()
		return false, ctx.Err()
	}
	data, err := s.bot.Raw("deleteMessage", map[string]string{
		"chat_id":    strconv.FormatInt(job.ChatID, 10),
		"message_id": strconv.FormatInt(job.MessageID, 10),
	})
	if err == nil {
		// Telebot Raw can report success for malformed JSON. Require an
		// explicit Telegram success instead of losing a pending deletion.
		var response struct {
			OK     bool `json:"ok"`
			Result bool `json:"result"`
		}
		if json.Unmarshal(data, &response) != nil || !response.OK || !response.Result {
			err = fmt.Errorf("invalid Telegram delete response")
		}
	}
	permanent := false
	var te *tele.Error
	if errors.As(err, &te) && te.Code >= 400 && te.Code < 500 && te.Code != 429 {
		permanent = true
	}
	detail := ""
	if err != nil {
		detail = redact.ErrorString(err)
	}
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if e := s.queries.SettleScheduledDelete(settle, *job, err == nil, permanent, detail); e != nil {
		return true, e
	}
	if err != nil {
		s.logger.Warn("scheduled message deletion failed", zap.Int64("delete_job_id", job.ID), zap.Error(err))
	}
	return true, nil
}
