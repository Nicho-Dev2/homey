package schedule

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/indexers"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/movies"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/shows"
)

// Imports lead to one Jellyfin scan, at most once a minute.
func TestJellyfinScanAfterImports(t *testing.T) {
	var scans atomic.Int32
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Library/Refresh" {
			scans.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer jf.Close()

	dir := t.TempDir()
	db, _ := store.Open(filepath.Join(dir, "data.json"))
	_ = db.Update(func(d *store.Data) error {
		d.Settings.Jellyfin = store.Jellyfin{URL: jf.URL, APIKey: "k"}
		d.Settings.Schedule = store.Schedule{} // no searches in this test
		return nil
	})
	idx := indexers.New(db, filepath.Join(dir, "defs"))
	sc := New(db, idx, movies.New(db, idx), shows.New(db, idx))
	now := time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC)
	sc.Now = func() time.Time { return now }
	ctx := context.Background()

	sc.Tick(ctx)
	if scans.Load() != 0 {
		t.Fatal("scanned with nothing imported")
	}
	sc.Movies.OnImport()
	sc.Shows.OnImport()
	sc.Tick(ctx)
	if scans.Load() != 1 {
		t.Fatalf("scans = %d, want 1", scans.Load())
	}
	sc.Shows.OnImport()
	now = now.Add(30 * time.Second)
	sc.Tick(ctx)
	if scans.Load() != 1 {
		t.Fatal("scanned again within a minute")
	}
	now = now.Add(time.Minute)
	sc.Tick(ctx)
	if scans.Load() != 2 {
		t.Fatalf("scans = %d, want 2", scans.Load())
	}
}
