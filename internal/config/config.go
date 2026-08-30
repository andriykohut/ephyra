// Package config turns environment variables into a Config. Nothing else reads
// the environment.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	JellyfinURL      string
	JellyfinAPIKey   string
	JellyfinDataDir  string
	Source           string
	StorePath        string
	WorkDir          string
	ListenAddr       string
	RefreshLibrary   time.Duration
	RefreshWatch     time.Duration
	LivePollInterval time.Duration
	DirectRead       bool
	StreamCapacity   int
	LogLevel         slog.Level
}

// Load reads config from getenv (pass os.Getenv). It returns one error listing
// every problem it found, not just the first.
func Load(getenv func(string) string) (Config, error) {
	c := Config{
		JellyfinURL:      getenv("JELLYFIN_URL"),
		JellyfinAPIKey:   getenv("JELLYFIN_API_KEY"),
		JellyfinDataDir:  getenv("JELLYFIN_DATA_DIR"),
		Source:           def(getenv("SOURCE"), "auto"),
		StorePath:        def(getenv("STORE_PATH"), "/data/ephyra.db"),
		WorkDir:          def(getenv("WORK_DIR"), "/data/work"),
		ListenAddr:       def(getenv("LISTEN_ADDR"), ":8080"),
		RefreshLibrary:   30 * time.Minute,
		RefreshWatch:     10 * time.Minute,
		LivePollInterval: 4 * time.Second,
	}

	var errs []string
	req := func(name, val string) {
		if strings.TrimSpace(val) == "" {
			errs = append(errs, "missing required env "+name)
		}
	}
	req("JELLYFIN_URL", c.JellyfinURL)
	req("JELLYFIN_API_KEY", c.JellyfinAPIKey)
	if c.Source != "api" {
		req("JELLYFIN_DATA_DIR", c.JellyfinDataDir)
	}
	if c.Source != "auto" && c.Source != "file" && c.Source != "api" {
		errs = append(errs, "SOURCE must be one of auto|file|api, got "+c.Source)
	}

	dur := func(name string, dst *time.Duration) {
		if v := getenv(name); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", name, err))
				return
			}
			*dst = d
		}
	}
	dur("REFRESH_LIBRARY", &c.RefreshLibrary)
	dur("REFRESH_WATCH", &c.RefreshWatch)
	dur("LIVE_POLL_INTERVAL", &c.LivePollInterval)

	if v := getenv("DIRECT_READ"); v != "" {
		c.DirectRead = v == "1" || strings.EqualFold(v, "true")
	}
	if v := getenv("STREAM_CAPACITY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			errs = append(errs, "STREAM_CAPACITY: "+err.Error())
		}
		c.StreamCapacity = n
	}
	switch strings.ToLower(def(getenv("LOG_LEVEL"), "info")) {
	case "debug":
		c.LogLevel = slog.LevelDebug
	case "info":
		c.LogLevel = slog.LevelInfo
	case "warn":
		c.LogLevel = slog.LevelWarn
	case "error":
		c.LogLevel = slog.LevelError
	default:
		errs = append(errs, "LOG_LEVEL must be debug|info|warn|error")
	}

	if len(errs) > 0 {
		return Config{}, errors.New(strings.Join(errs, "; "))
	}
	return c, nil
}

func def(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
