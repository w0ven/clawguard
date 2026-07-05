package bot

import (
	"testing"

	"github.com/openclaw/clawguard/internal/config"
)

func testUngraduatedPolicy(noMedia, noInvites bool) config.GuardPolicy {
	policy := config.DefaultPolicy
	policy.Filter.NewUser.Enabled = true
	policy.Filter.NewUser.NoMedia = noMedia
	policy.Filter.NewUser.NoInvites = noInvites
	return policy
}

func TestUngraduatedRestrictedRightsNoInvites(t *testing.T) {
	policy := testUngraduatedPolicy(false, true)
	rights := ungraduatedRestrictedRights(policy)
	unrestricted := normalMemberRights()

	if rights.CanInviteUsers {
		t.Fatal("CanInviteUsers = true, want false")
	}
	if rights.CanSendMedia != unrestricted.CanSendMedia {
		t.Fatalf("CanSendMedia = %t, want unchanged %t", rights.CanSendMedia, unrestricted.CanSendMedia)
	}
}

func TestUngraduatedRestrictedRightsNoMediaAndNoInvites(t *testing.T) {
	policy := testUngraduatedPolicy(true, true)
	rights := ungraduatedRestrictedRights(policy)

	if rights.CanInviteUsers {
		t.Fatal("CanInviteUsers = true, want false")
	}
	if rights.CanSendMedia || rights.CanSendAudios || rights.CanSendDocuments || rights.CanSendPhotos || rights.CanSendVideos || rights.CanSendVideoNotes || rights.CanSendVoiceNotes || rights.CanSendPolls || rights.CanSendOther || rights.CanAddPreviews {
		t.Fatalf("media rights were not fully restricted: %+v", rights)
	}
}

func TestNormalMemberRightsAllowsInvites(t *testing.T) {
	rights := normalMemberRights()
	if !rights.CanInviteUsers {
		t.Fatal("normalMemberRights CanInviteUsers = false, want true")
	}
}
