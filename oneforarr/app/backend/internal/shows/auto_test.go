package shows

import (
	"context"
	"testing"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
)

// Newest releases: a pack for a fully missing season is sent once; an
// episode release for a show we don't follow is ignored.
func TestMatchNewReleases(t *testing.T) {
	e := newEnv(t, `{"r":[]}`)
	ctx := context.Background()
	if _, err := e.s.Add(ctx, 77, "1080p", MonitorAll); err != nil {
		t.Fatal(err)
	}
	newest := []model.Release{
		{Title: "Other.Show.S01E01.1080p.WEB-DL.x264-GRP", MagnetURL: "magnet:?xt=urn:btih:o1&tr=x", Size: 1 << 30, Seeders: 50, Categories: []int{5000}},
		{Title: "Tiny.Show.S01E01.1080p.WEB-DL.x264-GRP", MagnetURL: "magnet:?xt=urn:btih:e1&tr=x", Size: 1 << 30, Seeders: 50, Categories: []int{5000}},
		{Title: "Tiny.Show.S01.1080p.WEB-DL.x264-GRP", MagnetURL: "magnet:?xt=urn:btih:p1&tr=x", Size: 3 << 30, Seeders: 50, Categories: []int{5000}},
	}
	if n := e.s.MatchNewReleases(ctx, newest); n != 1 {
		t.Fatalf("sent %d, want 1 (the pack); added %v", n, e.qb.added)
	}
	if len(e.qb.added) != 1 || e.qb.added[0] != "oneforarr-show-77-s01" {
		t.Errorf("added %v", e.qb.added)
	}
	// Seen again on the next check: nothing new is sent.
	if n := e.s.MatchNewReleases(ctx, newest); n != 0 || len(e.qb.added) != 1 {
		t.Errorf("second check sent %d: %v", n, e.qb.added)
	}
}

// The scheduled search runs when due, then waits for `every`.
func TestAutoSearchNext(t *testing.T) {
	e := newEnv(t, packAnswer)
	ctx := context.Background()
	if _, err := e.s.Add(ctx, 77, "1080p", MonitorAll); err != nil {
		t.Fatal(err)
	}
	if !e.s.AutoSearchNext(ctx, 12*time.Hour) {
		t.Fatal("first scheduled search didn't run")
	}
	if len(e.qb.added) != 1 {
		t.Fatalf("added %v", e.qb.added)
	}
	if e.s.AutoSearchNext(ctx, 12*time.Hour) {
		t.Error("searched again right away")
	}
	sh, _ := e.s.Get(77)
	if sh.LastSearch == nil || sh.AutoSearchedAt.IsZero() {
		t.Errorf("not noted: %+v", sh.LastSearch)
	}
}
