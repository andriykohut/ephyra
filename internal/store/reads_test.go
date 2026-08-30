package store

import (
	"testing"
	"time"
)

func TestIsStale(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	interval := 30 * time.Minute

	if !IsStale(RefreshMeta{}, false, interval, now) {
		t.Error("no row -> stale")
	}
	if !IsStale(RefreshMeta{OK: false, LastRunAt: now}, true, interval, now) {
		t.Error("last run failed -> stale")
	}
	if IsStale(RefreshMeta{OK: true, LastRunAt: now.Add(-20 * time.Minute)}, true, interval, now) {
		t.Error("fresh -> not stale")
	}
	if !IsStale(RefreshMeta{OK: true, LastRunAt: now.Add(-61 * time.Minute)}, true, interval, now) {
		t.Error("older than 2x interval -> stale")
	}
}
