package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandleLibraries(t *testing.T) {
	s, st, _ := newTestServer(t, time.Now())
	ctx := context.Background()

	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO dim_library (name) VALUES ('Movies'), ('Shows'), ('Unknown')`); err != nil {
		t.Fatal(err)
	}
	// Movies: 5 items, Shows: 10 items, Unknown: no agg_distribution row (0 items).
	if _, err := st.DB().ExecContext(ctx,
		`INSERT INTO agg_distribution (library, dimension, bucket, items) VALUES
			('', 'library_items', 'Movies', 5),
			('', 'library_items', 'Shows', 10)`); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/libraries", nil)
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data []string `json:"data"`
		Meta Meta     `json:"meta"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	// Sorted by item count desc, then name: Shows (10), Movies (5), Unknown (0).
	want := []string{"Shows", "Movies", "Unknown"}
	if len(env.Data) != len(want) {
		t.Fatalf("data = %v, want %v", env.Data, want)
	}
	for i, name := range want {
		if env.Data[i] != name {
			t.Fatalf("data = %v, want %v", env.Data, want)
		}
	}
	if env.Meta.Stale {
		t.Fatalf("expected stale=false, got %+v", env.Meta)
	}
	if env.Meta.GeneratedAt == "" {
		t.Fatal("expected generated_at to be set")
	}
}

func TestHandleLibraries_Empty(t *testing.T) {
	s, _, _ := newTestServer(t, time.Now())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/libraries", nil)
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data == nil {
		t.Fatal("data serialized as null; want []")
	}
	if len(env.Data) != 0 {
		t.Fatalf("data = %v, want empty", env.Data)
	}
}
