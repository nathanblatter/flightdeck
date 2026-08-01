package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5"

	"flightdeck/internal/store"
)

// Setting keys stored in the settings table (written by the setup wizard,
// see internal/api/setup.go).
const (
	SettingInstanceName  = "instance_name"
	SettingOpenAIKey     = "openai_api_key"
	SettingFlags         = "flags"
	SettingSetupComplete = "setup_complete"
)

// Feature flag names. Unset flags default to enabled.
const (
	FlagUsageAnalytics = "usage_analytics"
	FlagBugWidget      = "bug_widget"
)

// instanceSettings is the in-memory snapshot of the settings table, swapped
// atomically on reload so hot paths (RecordToolCall, /bug-widget.js) read it
// without a DB round-trip.
type instanceSettings struct {
	Name  string
	Flags map[string]bool
}

// ReloadSettings re-reads the settings table into the in-memory snapshot and
// pushes the stored OpenAI key into the embedder (a no-op when the key came
// from the environment — env always wins). Best-effort: a missing row is not
// an error, and a DB failure leaves the previous snapshot in place.
func (s *Service) ReloadSettings(ctx context.Context) {
	snap := &instanceSettings{Flags: map[string]bool{}}
	if name, ok := s.settingString(ctx, SettingInstanceName); ok {
		snap.Name = name
	}
	if row, err := s.St.GetSetting(ctx, SettingFlags); err == nil {
		_ = json.Unmarshal(row.Value, &snap.Flags)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		log.Printf("settings: load flags: %v", err)
	}
	if key, ok := s.settingString(ctx, SettingOpenAIKey); ok {
		s.emb.SetAPIKey(key)
	}
	s.settings.Store(snap)
}

// InstanceName returns the configured instance name, or "" when unset.
func (s *Service) InstanceName() string {
	if snap := s.settings.Load(); snap != nil {
		return snap.Name
	}
	return ""
}

// Flag reports a feature flag; flags default to enabled when never configured.
func (s *Service) Flag(name string) bool {
	snap := s.settings.Load()
	if snap == nil {
		return true
	}
	v, ok := snap.Flags[name]
	if !ok {
		return true
	}
	return v
}

// FlagsSnapshot returns a copy of the configured feature flags.
func (s *Service) FlagsSnapshot() map[string]bool {
	out := map[string]bool{
		FlagUsageAnalytics: s.Flag(FlagUsageAnalytics),
		FlagBugWidget:      s.Flag(FlagBugWidget),
	}
	return out
}

// EmbeddingEnabled reports whether semantic search has an API key (env or
// settings) — exposed so the settings UI can show state without the secret.
func (s *Service) EmbeddingEnabled() bool { return s.emb.Enabled() }

// SetupComplete reports whether initial setup has run. An instance with any
// active API key predates the wizard and counts as set up, so existing
// deployments never see the setup flow.
func (s *Service) SetupComplete(ctx context.Context) (bool, error) {
	if row, err := s.St.GetSetting(ctx, SettingSetupComplete); err == nil {
		var done bool
		if json.Unmarshal(row.Value, &done) == nil && done {
			return true, nil
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	n, err := s.St.CountActiveAPIKeys(ctx)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// PutSetting marshals and upserts one setting, then reloads the snapshot.
func (s *Service) PutSetting(ctx context.Context, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("settings: marshal %s: %w", key, err)
	}
	if err := s.St.UpsertSetting(ctx, store.UpsertSettingParams{Key: key, Value: b}); err != nil {
		return err
	}
	s.ReloadSettings(ctx)
	return nil
}

// settingString reads a setting expected to hold a JSON string.
func (s *Service) settingString(ctx context.Context, key string) (string, bool) {
	row, err := s.St.GetSetting(ctx, key)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Printf("settings: load %s: %v", key, err)
		}
		return "", false
	}
	var v string
	if err := json.Unmarshal(row.Value, &v); err != nil {
		return "", false
	}
	return v, true
}
