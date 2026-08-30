package aggregate

import (
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/source"
)

func TestCorePlays_TopNPerUserAndOrder(t *testing.T) {
	now := time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC)
	var plays []source.UserPlay
	for i := 0; i < 30; i++ {
		plays = append(plays, source.UserPlay{
			UserID: "a", Scope: "movie", ItemID: string(rune('A' + i)), Name: "M" + string(rune('a'+i)),
			PlayCount: 30 - i, LastPlayedAt: now,
		})
	}
	plays = append(plays, source.UserPlay{UserID: "b", Scope: "series", ItemID: "s1", Name: "Show", PlayCount: 5, LastPlayedAt: now})

	rows := CorePlays(plays)

	var a, b int
	for _, r := range rows {
		switch r.UserID {
		case "a":
			a++
		case "b":
			b++
		}
	}
	if a != 25 {
		t.Fatalf("user a capped at 25, got %d", a)
	}
	if b != 1 {
		t.Fatalf("user b = %d", b)
	}
	for _, r := range rows {
		if r.UserID == "a" {
			if r.PlayCount != 30 {
				t.Fatalf("first a row play_count = %d, want 30", r.PlayCount)
			}
			break
		}
	}
}
