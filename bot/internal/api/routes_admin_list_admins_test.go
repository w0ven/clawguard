package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/openclaw/clawguard/internal/store"
)

type fakeAdminLister struct {
	items []store.Admin
	err   error
	calls int
}

func (f *fakeAdminLister) ListAdmins(context.Context) ([]store.Admin, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func stringPtrValue(v string) *string { return &v }

// Roster used by the visibility tests:
//   - ownerAdmin        : role=owner
//   - globalAdmin       : role=admin, empty scope (this project treats empty as global)
//   - scopedAdminA      : role=admin, scope [-100, -200]  (the restricted viewer)
//   - scopedAdminOverlap: role=admin, scope [-200, -300]  (overlaps A on -200)
//   - scopedAdminOther  : role=admin, scope [-900]        (no overlap with A)
func listAdminsRoster() []store.Admin {
	return []store.Admin{
		{ID: 1, TelegramID: 11, Username: stringPtrValue("owner"), Role: "owner", Notes: stringPtrValue("owner notes"), GroupScope: []byte(`[]`)},
		{ID: 2, TelegramID: 22, Username: stringPtrValue("globaladmin"), Role: "admin", Notes: stringPtrValue("global notes"), GroupScope: []byte(`[]`)},
		{ID: 3, TelegramID: 33, Username: stringPtrValue("scopeda"), Role: "admin", Notes: stringPtrValue("scoped a notes"), GroupScope: []byte(`[-100,-200]`)},
		{ID: 4, TelegramID: 44, Username: stringPtrValue("overlap"), Role: "admin", Notes: stringPtrValue("overlap notes"), GroupScope: []byte(`[-200,-300]`)},
		{ID: 5, TelegramID: 55, Username: stringPtrValue("other"), Role: "admin", Notes: stringPtrValue("other notes"), GroupScope: []byte(`[-900]`)},
	}
}

type listedAdmin struct {
	ID         int64   `json:"id"`
	TelegramID int64   `json:"telegram_id"`
	Role       string  `json:"role"`
	Notes      *string `json:"notes"`
	GroupScope []int64 `json:"group_scope"`
}

func getListAdmins(t *testing.T, viewer store.Admin, queries adminLister) (*httptest.ResponseRecorder, []listedAdmin) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/admins", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(adminContextKey, viewer)
	if err := listAdminsFromContext(c, queries); err != nil {
		t.Fatalf("listAdminsFromContext: %v", err)
	}
	if rec.Code != http.StatusOK {
		return rec, nil
	}
	var payload struct {
		Admins []listedAdmin `json:"admins"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body: %v; body=%s", err, rec.Body.String())
	}
	return rec, payload.Admins
}

func visibleIDs(admins []listedAdmin) []int64 {
	ids := make([]int64, 0, len(admins))
	for _, a := range admins {
		ids = append(ids, a.ID)
	}
	return ids
}

func assertIDs(t *testing.T, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("visible admin ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("visible admin ids = %v, want %v", got, want)
		}
	}
}

func TestListAdminsOwnerSeesFullRoster(t *testing.T) {
	fake := &fakeAdminLister{items: listAdminsRoster()}
	owner := store.Admin{ID: 1, TelegramID: 11, Role: "owner", GroupScope: []byte(`[]`)}

	_, admins := getListAdmins(t, owner, fake)

	assertIDs(t, visibleIDs(admins), []int64{1, 2, 3, 4, 5})
	for _, a := range admins {
		if a.Notes == nil {
			t.Fatalf("owner must keep notes for admin %d", a.ID)
		}
	}
	// Owner keeps the raw scopes untouched.
	for _, a := range admins {
		if a.ID == 5 && (len(a.GroupScope) != 1 || a.GroupScope[0] != -900) {
			t.Fatalf("owner group_scope for admin 5 = %v, want [-900]", a.GroupScope)
		}
	}
}

func TestListAdminsGlobalAdminSeesFullRoster(t *testing.T) {
	fake := &fakeAdminLister{items: listAdminsRoster()}
	// role=admin with empty scope: this project's "global" admin.
	globalAdmin := store.Admin{ID: 2, TelegramID: 22, Role: "admin", GroupScope: []byte(`[]`)}

	_, admins := getListAdmins(t, globalAdmin, fake)

	assertIDs(t, visibleIDs(admins), []int64{1, 2, 3, 4, 5})
	for _, a := range admins {
		if a.Notes == nil {
			t.Fatalf("global admin must keep notes for admin %d", a.ID)
		}
	}
}

func TestListAdminsScopedAdminSeesOnlyOverlapAndGlobals(t *testing.T) {
	fake := &fakeAdminLister{items: listAdminsRoster()}
	scoped := store.Admin{ID: 3, TelegramID: 33, Role: "admin", GroupScope: []byte(`[-100,-200]`)}

	_, admins := getListAdmins(t, scoped, fake)

	// owner(1) + global admin(2) + self(3) + overlapping scoped admin(4).
	// The non-overlapping scoped admin(5) must be invisible.
	assertIDs(t, visibleIDs(admins), []int64{1, 2, 3, 4})

	byID := map[int64]listedAdmin{}
	for _, a := range admins {
		byID[a.ID] = a
	}
	if _, leaked := byID[5]; leaked {
		t.Fatal("scoped admin must not enumerate out-of-scope admin 5")
	}
	for _, a := range admins {
		if a.TelegramID == 55 {
			t.Fatal("out-of-scope admin telegram id leaked")
		}
	}

	// notes of other admins are redacted; own notes are preserved.
	if byID[1].Notes != nil {
		t.Fatalf("owner notes leaked to scoped admin: %v", *byID[1].Notes)
	}
	if byID[2].Notes != nil {
		t.Fatalf("global admin notes leaked to scoped admin: %v", *byID[2].Notes)
	}
	if byID[4].Notes != nil {
		t.Fatalf("peer admin notes leaked to scoped admin: %v", *byID[4].Notes)
	}
	if byID[3].Notes == nil || *byID[3].Notes != "scoped a notes" {
		t.Fatalf("scoped admin must keep its own notes, got %v", byID[3].Notes)
	}

	// Peer scope is narrowed to the shared groups only: -300 is out of scope.
	peerScope := byID[4].GroupScope
	if len(peerScope) != 1 || peerScope[0] != -200 {
		t.Fatalf("peer group_scope = %v, want [-200]", peerScope)
	}

	// Global targets still render as unrestricted (empty scope) for the UI.
	if len(byID[1].GroupScope) != 0 {
		t.Fatalf("owner group_scope = %v, want []", byID[1].GroupScope)
	}
	if len(byID[2].GroupScope) != 0 {
		t.Fatalf("global admin group_scope = %v, want []", byID[2].GroupScope)
	}

	// Own record keeps the full scope.
	ownScope := byID[3].GroupScope
	if len(ownScope) != 2 || ownScope[0] != -100 || ownScope[1] != -200 {
		t.Fatalf("own group_scope = %v, want [-100 -200]", ownScope)
	}
}

func TestListAdminsScopedAdminWithNoPeersStillSeesSelfAndGlobals(t *testing.T) {
	fake := &fakeAdminLister{items: listAdminsRoster()}
	scoped := store.Admin{ID: 5, TelegramID: 55, Role: "admin", GroupScope: []byte(`[-900]`)}

	_, admins := getListAdmins(t, scoped, fake)

	assertIDs(t, visibleIDs(admins), []int64{1, 2, 5})
}

func TestListAdminsStoreErrorReturns500(t *testing.T) {
	fake := &fakeAdminLister{err: errors.New("boom")}
	rec, _ := getListAdmins(t, store.Admin{ID: 1, Role: "owner", GroupScope: []byte(`[]`)}, fake)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestListAdminsEmptyRosterSerializesAsArray(t *testing.T) {
	fake := &fakeAdminLister{items: nil}
	rec, _ := getListAdmins(t, store.Admin{ID: 1, Role: "owner", GroupScope: []byte(`[]`)}, fake)
	if got := rec.Body.String(); !json.Valid([]byte(got)) {
		t.Fatalf("invalid json: %s", got)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(payload["admins"]) != "[]" {
		t.Fatalf("admins = %s, want [] (not null)", string(payload["admins"]))
	}
}
