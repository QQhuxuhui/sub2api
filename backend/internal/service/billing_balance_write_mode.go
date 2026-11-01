package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// SettingKeyBillingBalanceWriteMode holds the JSON BillingBalanceWriteConfig.
	SettingKeyBillingBalanceWriteMode = "billing_balance_write_mode"

	BillingBalanceWriteModeSync = "sync"
	BillingBalanceWriteModeList = "list"
	BillingBalanceWriteModeAll  = "all"

	billingBalanceWriteModeTTL = 30 * time.Second
)

// BillingBalanceWriteConfig selects who is billed through user_balance_pending.
type BillingBalanceWriteConfig struct {
	Mode    string  `json:"mode"`
	UserIDs []int64 `json:"user_ids"`
}

// ParseBillingBalanceWriteConfig reads the setting value. An empty value is sync;
// so is a value it can not understand, reported as an error.
func ParseBillingBalanceWriteConfig(raw string) (BillingBalanceWriteConfig, error) {
	syncConfig := BillingBalanceWriteConfig{Mode: BillingBalanceWriteModeSync}
	if strings.TrimSpace(raw) == "" {
		return syncConfig, nil
	}
	var cfg BillingBalanceWriteConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return syncConfig, fmt.Errorf("parse %s: %w", SettingKeyBillingBalanceWriteMode, err)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case BillingBalanceWriteModeList:
		cfg.Mode = BillingBalanceWriteModeList
	case BillingBalanceWriteModeAll:
		cfg.Mode = BillingBalanceWriteModeAll
	case BillingBalanceWriteModeSync, "":
		cfg.Mode = BillingBalanceWriteModeSync
	default:
		return syncConfig, fmt.Errorf("parse %s: unknown mode %q", SettingKeyBillingBalanceWriteMode, cfg.Mode)
	}
	return cfg, nil
}

type billingBalanceWriteSnapshot struct {
	cfg      BillingBalanceWriteConfig
	users    map[int64]struct{}
	loadedAt time.Time
}

func (s *billingBalanceWriteSnapshot) defers(userID int64) bool {
	switch s.cfg.Mode {
	case BillingBalanceWriteModeAll:
		return true
	case BillingBalanceWriteModeList:
		_, listed := s.users[userID]
		return listed
	default:
		return false
	}
}

// BillingBalanceWriteModeService keeps the setting in an in-memory snapshot,
// refreshed in the background every 30 seconds.
type BillingBalanceWriteModeService struct {
	settings SettingRepository
	now      func() time.Time

	snapshot  atomic.Pointer[billingBalanceWriteSnapshot]
	reloading atomic.Bool
}

// NewBillingBalanceWriteModeService builds the service.
func NewBillingBalanceWriteModeService(settings SettingRepository) *BillingBalanceWriteModeService {
	return &BillingBalanceWriteModeService{settings: settings, now: time.Now}
}

// ShouldDefer reports whether the user's balance deductions go to the pending table.
func (s *BillingBalanceWriteModeService) ShouldDefer(ctx context.Context, userID int64) bool {
	if s == nil {
		return false
	}
	return s.current(ctx).defers(userID)
}

// Mode returns the current mode from the snapshot.
func (s *BillingBalanceWriteModeService) Mode(ctx context.Context) string {
	if s == nil {
		return BillingBalanceWriteModeSync
	}
	return s.current(ctx).cfg.Mode
}

// Refresh reads the setting now. On a read failure it returns the error together
// with the mode requests keep using, so a caller can tell a known mode from a
// fallback.
func (s *BillingBalanceWriteModeService) Refresh(ctx context.Context) (string, error) {
	if s == nil {
		return BillingBalanceWriteModeSync, nil
	}
	snapshot, err := s.reload(ctx)
	return snapshot.cfg.Mode, err
}

func (s *BillingBalanceWriteModeService) current(ctx context.Context) *billingBalanceWriteSnapshot {
	snapshot := s.snapshot.Load()
	if snapshot == nil {
		snapshot, _ = s.reload(ctx)
		return snapshot
	}
	if s.now().Sub(snapshot.loadedAt) >= billingBalanceWriteModeTTL && s.reloading.CompareAndSwap(false, true) {
		go func() {
			defer s.reloading.Store(false)
			reloadCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = s.reload(reloadCtx)
		}()
	}
	return snapshot
}

// reload keeps the previous snapshot when the setting can not be read; with no
// previous snapshot the mode is sync.
func (s *BillingBalanceWriteModeService) reload(ctx context.Context) (*billingBalanceWriteSnapshot, error) {
	raw, err := s.settings.GetValue(ctx, SettingKeyBillingBalanceWriteMode)
	if errors.Is(err, ErrSettingNotFound) {
		raw, err = "", nil
	}
	if err != nil {
		slog.Warn("billing.balance_write_mode_load_failed", "error", err)
		previous := s.snapshot.Load()
		if previous == nil {
			return s.store(BillingBalanceWriteConfig{Mode: BillingBalanceWriteModeSync}), err
		}
		refreshed := *previous
		refreshed.loadedAt = s.now()
		s.snapshot.Store(&refreshed)
		return &refreshed, err
	}
	cfg, parseErr := ParseBillingBalanceWriteConfig(raw)
	if parseErr != nil {
		slog.Error("billing.balance_write_mode_invalid", "error", parseErr)
	}
	return s.store(cfg), nil
}

func (s *BillingBalanceWriteModeService) store(cfg BillingBalanceWriteConfig) *billingBalanceWriteSnapshot {
	snapshot := &billingBalanceWriteSnapshot{
		cfg:      cfg,
		users:    make(map[int64]struct{}, len(cfg.UserIDs)),
		loadedAt: s.now(),
	}
	for _, id := range cfg.UserIDs {
		snapshot.users[id] = struct{}{}
	}
	previous := s.snapshot.Swap(snapshot)
	if previous == nil || previous.cfg.Mode != cfg.Mode || !sameBalanceWriteUsers(previous.users, snapshot.users) {
		// The rollback procedure confirms the switch from this line.
		slog.Info("billing.balance_write_mode_changed", "mode", cfg.Mode, "user_ids", cfg.UserIDs)
	}
	return snapshot
}

func sameBalanceWriteUsers(a, b map[int64]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if _, ok := b[id]; !ok {
			return false
		}
	}
	return true
}
