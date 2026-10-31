//go:build integration

package api

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/openclaw/clawguard/internal/store"
)

type racingAuthorizedGroupWriter struct {
	q       *store.Queries
	barrier sync.WaitGroup
}

func (r *racingAuthorizedGroupWriter) GetAuthorizedGroupByChatID(ctx context.Context, id int64) (store.AuthorizedGroup, error) {
	g, e := r.q.GetAuthorizedGroupByChatID(ctx, id)
	r.barrier.Done()
	r.barrier.Wait()
	return g, e
}
func (r *racingAuthorizedGroupWriter) InsertAuthorizedGroupIfAbsent(ctx context.Context, p store.UpsertAuthorizedGroupParams) (store.AuthorizedGroup, error) {
	return r.q.InsertAuthorizedGroupIfAbsent(ctx, p)
}

func TestAuthorizedGroupConcurrentCreateDoesNotOverwrite(t *testing.T) {
	fx := openH1Fixture(t)
	ctx := context.Background()
	r := &racingAuthorizedGroupWriter{q: fx.queries}
	r.barrier.Add(2)
	type result struct {
		group store.AuthorizedGroup
		err   error
	}
	ch := make(chan result, 2)
	for _, enabled := range []bool{true, false} {
		go func(v bool) {
			title, notes := "candidate", "kept"
			g, e := createAuthorizedGroupIfAbsent(ctx, r, store.UpsertAuthorizedGroupParams{ChatID: -777, Title: &title, Notes: &notes, Enabled: &v})
			ch <- result{g, e}
		}(enabled)
	}
	a, b := <-ch, <-ch
	var winner store.AuthorizedGroup
	if a.err == nil && errors.Is(b.err, errAuthorizedGroupAlreadyExists) {
		winner = a.group
	} else if b.err == nil && errors.Is(a.err, errAuthorizedGroupAlreadyExists) {
		winner = b.group
	} else {
		t.Fatalf("expected one success one conflict: %v %v", a.err, b.err)
	}
	stored, err := fx.queries.GetAuthorizedGroupByChatID(ctx, -777)
	if err != nil || stored.Enabled != winner.Enabled || stored.Notes != winner.Notes {
		t.Fatalf("overwritten: winner=%+v stored=%+v error=%v", winner, stored, err)
	}
}
