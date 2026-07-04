package service

import (
	"context"
	"testing"
	"time"

	"github.com/kleinai/backend/internal/model"
)

func TestReserveWhereAllowsConcurrentCustomUpstreamAccount(t *testing.T) {
	base := "https://pic2api.com"
	acc := &model.Account{
		ID:       189,
		Provider: model.ProviderGPT,
		AuthType: model.AuthTypeAPIKey,
		BaseURL:  &base,
	}
	pool := &AccountPool{
		cacheTTL: time.Hour,
		buckets: map[string]*providerBucket{
			model.ProviderGPT: {
				loadedAt: time.Now(),
				items:    []*model.Account{acc},
			},
		},
		busy: map[uint64]struct{}{acc.ID: {}},
	}

	got, err := pool.ReserveWhere(context.Background(), model.ProviderGPT, "round_robin", nil)
	if err != nil {
		t.Fatalf("ReserveWhere() error = %v", err)
	}
	if got == nil || got.ID != acc.ID {
		t.Fatalf("ReserveWhere() got %#v, want account %d", got, acc.ID)
	}
}

func TestReserveWhereStillBlocksBusyRegularAccount(t *testing.T) {
	acc := &model.Account{
		ID:       1,
		Provider: model.ProviderGPT,
		AuthType: model.AuthTypeOAuth,
	}
	pool := &AccountPool{
		cacheTTL: time.Hour,
		buckets: map[string]*providerBucket{
			model.ProviderGPT: {
				loadedAt: time.Now(),
				items:    []*model.Account{acc},
			},
		},
		busy: map[uint64]struct{}{acc.ID: {}},
	}

	if _, err := pool.ReserveWhere(context.Background(), model.ProviderGPT, "round_robin", nil); err == nil {
		t.Fatal("expected busy regular account to remain exclusive")
	}
}

func TestPickWherePredicateKeepsRoundRobinCursor(t *testing.T) {
	accounts := []*model.Account{
		{ID: 1, Provider: model.ProviderGROK},
		{ID: 2, Provider: model.ProviderGROK},
		{ID: 3, Provider: model.ProviderGROK},
		{ID: 4, Provider: model.ProviderGROK},
	}
	pool := &AccountPool{
		cacheTTL: time.Hour,
		buckets: map[string]*providerBucket{
			model.ProviderGROK: {
				loadedAt: time.Now(),
				items:    accounts,
			},
		},
		busy: map[uint64]struct{}{},
	}
	onlyEven := func(acc *model.Account) bool {
		return acc.ID%2 == 0
	}

	first, err := pool.PickWhere(context.Background(), model.ProviderGROK, "round_robin", onlyEven)
	if err != nil {
		t.Fatalf("first PickWhere() error = %v", err)
	}
	second, err := pool.PickWhere(context.Background(), model.ProviderGROK, "round_robin", onlyEven)
	if err != nil {
		t.Fatalf("second PickWhere() error = %v", err)
	}
	third, err := pool.PickWhere(context.Background(), model.ProviderGROK, "round_robin", onlyEven)
	if err != nil {
		t.Fatalf("third PickWhere() error = %v", err)
	}
	if first.ID != 2 || second.ID != 4 || third.ID != 2 {
		t.Fatalf("filtered round robin got %d,%d,%d; want 2,4,2", first.ID, second.ID, third.ID)
	}
}
