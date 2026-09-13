package jellyfin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andriykohut/ephyra/internal/config"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	dir, _ := os.Getwd()
	for {
		p := filepath.Join(dir, "testdata", name)
		if b, err := os.ReadFile(p); err == nil {
			return b
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("fixture %s not found", name)
		}
		dir = parent
	}
}

func TestClient_SessionsAndSystemInfo(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/Sessions":
			w.Write(fixture(t, "sessions.directplay.json"))
		case "/System/Info":
			w.Write(fixture(t, "systeminfo.json"))
		default:
			http.Error(w, "no", 404)
		}
	}))
	defer srv.Close()

	c := New(config.Config{JellyfinURL: srv.URL, JellyfinAPIKey: "K"})

	ss, err := c.Sessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) != 1 || ss[0].NowPlayingItem == nil || ss[0].PlayState == nil {
		t.Fatalf("sessions: %+v", ss)
	}
	if ss[0].PlayState.PlayMethod != "DirectPlay" || ss[0].NowPlayingItem.SeriesName != "Northwind" {
		t.Fatalf("parse: %+v", ss[0])
	}
	if gotAuth != "MediaBrowser Token=K" {
		t.Fatalf("auth header = %q", gotAuth)
	}

	si, err := c.SystemInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if si.Name == "" || si.Version == "" {
		t.Fatalf("system info: %+v", si)
	}
}

func TestClient_Error500(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()
	c := New(config.Config{JellyfinURL: srv.URL, JellyfinAPIKey: "K"})
	if _, err := c.SystemInfo(context.Background()); err == nil {
		t.Fatal("expected error on 500")
	}
}

func TestFindPersonRequiresAnExactNameMatch(t *testing.T) {
	// searchTerm is a substring match on the server: "Cranston" comes back with
	// both of these. Taking the first hit would link to the wrong person.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[
			{"Name":"Cranston Johnson","Id":"wrongid"},
			{"Name":"Bryan Cranston","Id":"rightid"}
		]}`)
	}))
	defer srv.Close()
	c := New(config.Config{JellyfinURL: srv.URL, JellyfinAPIKey: "key"})

	got, err := c.FindPerson(context.Background(), "Bryan Cranston")
	if err != nil {
		t.Fatal(err)
	}
	if got != "rightid" {
		t.Fatalf("id = %q, want rightid", got)
	}
}

func TestFindPersonReturnsEmptyWhenNoExactMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Name":"Cranston Johnson","Id":"wrongid"}]}`)
	}))
	defer srv.Close()
	c := New(config.Config{JellyfinURL: srv.URL, JellyfinAPIKey: "key"})

	got, err := c.FindPerson(context.Background(), "Bryan Cranston")
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("id = %q, want empty", got)
	}
}

func TestPersonImageEscapesTheName(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "image/jpeg")
	}))
	defer srv.Close()
	c := New(config.Config{JellyfinURL: srv.URL, JellyfinAPIKey: "key"})

	rc, _, err := c.PersonImage(context.Background(), "A$AP Rocky")
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if gotPath != "/Persons/A$AP Rocky/Images/Primary" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestUserImageEscapesTheID(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "image/jpeg")
	}))
	defer srv.Close()
	c := New(config.Config{JellyfinURL: srv.URL, JellyfinAPIKey: "key"})

	rc, _, err := c.UserImage(context.Background(), "abc 123")
	if err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if gotPath != "/Users/abc 123/Images/Primary" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestGenresMapsNameToID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Items":[{"Name":"Drama","Id":"d1"},{"Name":"Comedy","Id":"c1"}]}`)
	}))
	defer srv.Close()
	c := New(config.Config{JellyfinURL: srv.URL, JellyfinAPIKey: "key"})

	got, err := c.Genres(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got["Drama"] != "d1" || got["Comedy"] != "c1" || len(got) != 2 {
		t.Fatalf("genres = %+v", got)
	}
}

func TestServerIDReadsSystemInfoID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Id":"srv-1"}`)
	}))
	defer srv.Close()
	c := New(config.Config{JellyfinURL: srv.URL, JellyfinAPIKey: "key"})

	got, err := c.ServerID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "srv-1" {
		t.Fatalf("server id = %q, want srv-1", got)
	}
}

func TestClient_ImageAndTimeout(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\nfake")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Items/slow/Images/Primary" {
			time.Sleep(5 * time.Second)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(png)
	}))
	defer srv.Close()
	c := New(config.Config{JellyfinURL: srv.URL, JellyfinAPIKey: "K"})

	body, ct, err := c.Image(context.Background(), "abc123", ImagePrimary, "t1")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(body)
	body.Close()
	if ct != "image/png" || string(got) != string(png) {
		t.Fatalf("image: ct=%q body=%q", ct, got)
	}

	// the 3s client timeout fires before the 5s handler
	start := time.Now()
	if _, _, err := c.Image(context.Background(), "slow", ImagePrimary, ""); err == nil || time.Since(start) > 4*time.Second {
		t.Fatalf("3s timeout not honored: err=%v dur=%v", err, time.Since(start))
	}
}
