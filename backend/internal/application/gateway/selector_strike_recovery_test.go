package gateway

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/chenyme/grok2api/backend/internal/domain/account"
	"github.com/chenyme/grok2api/backend/internal/infra/runtime/memory"
	"github.com/chenyme/grok2api/backend/internal/infra/persistence/relational"
)

// A thinking strike must not outlive its cooldown window: after the window
// passes and the account delivers a healthy response, the strike clears, so
// a later isolated no-reasoning hit starts a fresh cooldown instead of
// escalating straight to a permanent disable.
func TestMissingThinkingStrikeExpiresAfterCooldown(t *testing.T) {
	ctx := context.Background()
	database, err := relational.OpenSQLite(ctx, filepath.Join(t.TempDir(), "strike-expiry.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.InitializeSchema(ctx); err != nil {
		t.Fatal(err)
	}
	accounts := relational.NewAccountRepository(database)
	credential, _, err := accounts.UpsertByIdentity(ctx, account.Credential{
		Provider: account.ProviderBuild, Name: "episodic", SourceKey: "episodic", EncryptedAccessToken: "encrypted", Enabled: true,
		AuthStatus: account.AuthStatusActive, Priority: 10, MaxConcurrent: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	selector := NewSelector(accounts, memory.NewConcurrencyLimiter(), memory.NewStickyStore(), nil, time.Hour, 30*time.Second, 30*time.Minute, 500*time.Millisecond)

	if action, err := selector.markMissingThinking(ctx, credential, time.Hour); err != nil || action != missingThinkingPenaltyCooled {
		t.Fatalf("first penalty = (%s, %v)", action, err)
	}
	struck, err := accounts.Get(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Success while the cooldown is still open keeps the strike (diagnostic
	// probes during the window must not launder the strike away).
	selector.MarkSuccess(ctx, struck)
	during, err := accounts.Get(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if during.LastError != lastErrorMissingThinking {
		t.Fatalf("success during cooldown must keep strike, got %q", during.LastError)
	}

	// Cooldown expires; the next healthy delivery clears the strike.
	expired := time.Now().UTC().Add(-time.Second)
	during.CooldownUntil = &expired
	selector.MarkSuccess(ctx, during)
	after, err := accounts.Get(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastError != "" {
		t.Fatalf("success after cooldown expiry must clear strike, got %q", after.LastError)
	}

	// A later isolated hit starts a fresh cooldown, not a disable.
	if action, err := selector.markMissingThinking(ctx, after, time.Hour); err != nil || action != missingThinkingPenaltyCooled {
		t.Fatalf("post-recovery penalty = (%s, %v), want cooled", action, err)
	}
	enabled, err := accounts.Get(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled {
		t.Fatal("post-recovery hit must not disable the account")
	}

	// Contrast: two hits with no healthy delivery between them still disable.
	struck2, err := accounts.Get(ctx, credential.ID)
	if err != nil {
		t.Fatal(err)
	}
	expired2 := time.Now().UTC().Add(-time.Second)
	struck2.CooldownUntil = &expired2
	if action, err := selector.markMissingThinking(ctx, struck2, time.Hour); err != nil || action != missingThinkingPenaltyDisabled {
		t.Fatalf("sustained penalty = (%s, %v), want disabled", action, err)
	}
}
