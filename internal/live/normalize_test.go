package live

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/andriykohut/ephyra/internal/jellyfin"
)

func loadSessions(t *testing.T, name string) []jellyfin.RawSession {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		p := filepath.Join(dir, "testdata", name)
		if b, err := os.ReadFile(p); err == nil {
			var ss []jellyfin.RawSession
			if err := json.Unmarshal(b, &ss); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return ss
		}
		if filepath.Dir(dir) == dir {
			t.Fatalf("%s not found", name)
		}
		dir = filepath.Dir(dir)
	}
}

func TestNormalize_DirectPlayEpisode(t *testing.T) {
	raw := loadSessions(t, "sessions.directplay.json")
	snap := normalize(raw, ServerInfo{Name: "S", Version: "10.11.11"}, nil)

	if len(snap.Sessions) != 1 {
		t.Fatalf("sessions = %d", len(snap.Sessions))
	}
	s := snap.Sessions[0]
	if s.Title != "The Long Retreat" || s.Series != "Northwind" || s.SeasonEpisode != "S1E7" || s.Type != "Episode" {
		t.Fatalf("labels: %+v", s)
	}
	if s.PlayMethod != "DirectPlay" || s.Paused {
		t.Fatalf("state: %+v", s)
	}
	if s.RuntimeSec != 3360 || s.PositionSec != 2522 {
		t.Fatalf("times: pos=%d run=%d", s.PositionSec, s.RuntimeSec)
	}
	if s.ProgressPct < 75.0 || s.ProgressPct > 75.1 {
		t.Fatalf("progress = %v", s.ProgressPct)
	}
	if s.IsRemote {
		t.Fatalf("192.168.* is local")
	}
	if s.Source.Video.Codec != "hevc" || s.Source.Video.Width != 1920 || s.Source.Video.Range != "SDR" {
		t.Fatalf("video: %+v", s.Source.Video)
	}
	if s.Source.Audio.Codec != "aac" || s.Source.Audio.Channels != 6 || s.Source.Audio.Layout != "5.1" {
		t.Fatalf("audio: %+v", s.Source.Audio)
	}
	if s.Transcode != nil {
		t.Fatalf("no transcode expected: %+v", s.Transcode)
	}
	if s.Art.PrimaryTag == "" || s.Art.Backdrop == nil || s.Art.Backdrop.ItemID == "" {
		t.Fatalf("art: %+v", s.Art)
	}
	if snap.Summary.Streams != 1 || snap.Summary.Transcodes != 0 {
		t.Fatalf("summary: %+v", snap.Summary)
	}
	if snap.Summary.OutboundBitrate != 4417693+480014 {
		t.Fatalf("outbound = %d", snap.Summary.OutboundBitrate)
	}
	// nil slices must marshal as []
	b, _ := json.Marshal(snap)
	if !contains(string(b), `"sessions":[`) {
		t.Fatalf("sessions should be an array: %s", b)
	}
}

func TestNormalize_Transcode(t *testing.T) {
	raw := loadSessions(t, "sessions.transcode.json")
	snap := normalize(raw, ServerInfo{}, ptr(6))
	if len(snap.Sessions) != 1 {
		t.Fatalf("want 1 session")
	}
	tc := snap.Sessions[0].Transcode
	if tc == nil {
		t.Fatalf("expected transcode")
	}
	if tc.Video == "" || tc.Bitrate == 0 || tc.Reasons == nil {
		t.Fatalf("transcode: %+v", tc)
	}
	if snap.Summary.Transcodes != 1 || snap.Summary.OutboundBitrate != tc.Bitrate {
		t.Fatalf("summary: %+v", snap.Summary)
	}
	if snap.Summary.Capacity == nil || *snap.Summary.Capacity != 6 {
		t.Fatalf("capacity: %+v", snap.Summary.Capacity)
	}
}

func TestIsRemote(t *testing.T) {
	cases := map[string]bool{
		"192.168.1.5": false, "10.0.0.9": false, "172.20.0.1": false,
		"127.0.0.1": false, "::1": false, "fe80::1": false,
		"8.8.8.8": true, "1.1.1.1": true,
		"203.0.113.7:52344": true, "192.168.1.5:9999": false,
		"garbage": false, "": false,
	}
	for in, want := range cases {
		if got := isRemote(in); got != want {
			t.Errorf("isRemote(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestSeasonEpisode(t *testing.T) {
	e7, s1 := 7, 1
	if got := seasonEpisode(&jellyfin.RawItem{Type: "Episode", IndexNumber: &e7, ParentIndexNumber: &s1}); got != "S1E7" {
		t.Errorf("episode = %q", got)
	}
	if got := seasonEpisode(&jellyfin.RawItem{Type: "Movie"}); got != "" {
		t.Errorf("movie = %q", got)
	}
}

func ptr(n int) *int { return &n }
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
