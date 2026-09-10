package bot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/openclaw/clawguard/internal/store"
)

type nativeMigrationProfile struct {
	PolicyVersion  int64  `json:"legacy_policy_version"`
	ProfileHash    string `json:"profile_sha256"`
	Target         int64  `json:"target_user_id"`
	SampleCount    int32  `json:"sample_count"`
	DistilledCount int32  `json:"distilled_at_count"`
}

func nativeMigrationProfileMatches(expected nativeMigrationProfile, current store.GroupAssistantPolicy) bool {
	hash := sha256.Sum256([]byte(current.MimicProfileText))
	return expected.PolicyVersion == current.Version && expected.ProfileHash == hex.EncodeToString(hash[:]) &&
		expected.Target == current.MimicTargetUserID && expected.SampleCount == current.MimicSampleCount && expected.DistilledCount == current.MimicDistilledAtCount
}

func (n *NativeAssistant) verifyMigrationProfiles(ctx context.Context, profiles map[string]nativeMigrationProfile) error {
	if len(profiles) == 0 {
		return errors.New("migration profiles required")
	}
	for raw, expected := range profiles {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id >= 0 {
			return errors.New("invalid migration group")
		}
		authorized, err := n.a.queries.GetAuthorizedGroupByChatID(ctx, id)
		if err != nil || !authorized.Enabled {
			return errors.New("migration group authorization changed")
		}
		current, err := n.a.queries.GetGroupAssistantPolicy(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) && expected.PolicyVersion == 0 {
			current = store.GroupAssistantPolicy{}
			err = nil
		}
		if err != nil {
			return err
		}
		if !nativeMigrationProfileMatches(expected, current) {
			return errors.New("migration profile changed; prepare latest paused snapshot")
		}
	}
	return nil
}
