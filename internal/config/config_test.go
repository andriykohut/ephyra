package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{
		"JELLYFIN_URL":      "http://jellyfin:8096",
		"JELLYFIN_API_KEY":  "k",
		"JELLYFIN_DATA_DIR": "/jellyfin-data",
	}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.StorePath != "/data/ephyra.db" || c.WorkDir != "/data/work" || c.ListenAddr != ":8080" {
		t.Fatalf("bad defaults: %+v", c)
	}
	if c.RefreshLibrary != 30*time.Minute || c.RefreshWatch != 10*time.Minute || c.LivePollInterval != 4*time.Second {
		t.Fatalf("bad duration defaults: %+v", c)
	}
	if c.Source != "auto" || c.DirectRead || c.LogLevel != slog.LevelInfo {
		t.Fatalf("bad misc defaults: %+v", c)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	_, err := Load(env(map[string]string{"SOURCE": "file"}))
	if err == nil {
		t.Fatal("expected error for missing required vars")
	}
	for _, want := range []string{"JELLYFIN_URL", "JELLYFIN_API_KEY", "JELLYFIN_DATA_DIR"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestLoadDataDirOptionalForAPISource(t *testing.T) {
	_, err := Load(env(map[string]string{
		"JELLYFIN_URL": "u", "JELLYFIN_API_KEY": "k", "SOURCE": "api",
	}))
	if err != nil {
		t.Fatalf("data dir should be optional when SOURCE=api: %v", err)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := Load(env(map[string]string{
		"JELLYFIN_URL": "u", "JELLYFIN_API_KEY": "k", "JELLYFIN_DATA_DIR": "/d",
		"REFRESH_LIBRARY": "5m", "LOG_LEVEL": "debug", "DIRECT_READ": "true",
		"STREAM_CAPACITY": "6", "LISTEN_ADDR": ":9000",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.RefreshLibrary != 5*time.Minute || c.LogLevel != slog.LevelDebug || !c.DirectRead || c.StreamCapacity != 6 || c.ListenAddr != ":9000" {
		t.Fatalf("overrides not applied: %+v", c)
	}
}
