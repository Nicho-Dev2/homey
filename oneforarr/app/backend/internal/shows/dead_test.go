package shows

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

// A season pack nobody seeds is given up on after the set hours: removed
// from qBittorrent, blocked, and the season searched again (episodes now).
func TestGiveUpOnDeadDownload(t *testing.T) {
	e := newEnv(t, packAnswer)
	ctx := context.Background()
	if _, err := e.s.Add(ctx, 77, "1080p", MonitorAll); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SearchAndGrab(ctx, 77, Target{Season: 1}); err != nil {
		t.Fatal(err)
	}
	e.qb.mu.Lock()
	e.qb.state = "stalledDL"
	e.qb.mu.Unlock()

	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	e.s.Now = func() time.Time { return now }
	_ = e.s.CheckDownloads(ctx) // starts the clock
	now = now.Add(5 * time.Hour)
	_ = e.s.CheckDownloads(ctx)
	if len(e.qb.deleted) != 0 {
		t.Fatal("gave up too soon")
	}
	now = now.Add(time.Hour)
	_ = e.s.CheckDownloads(ctx)

	e.qb.mu.Lock()
	deleted := slices.Clone(e.qb.deleted)
	e.qb.mu.Unlock()
	if len(deleted) != 1 || deleted[0] != "h-oneforarr-show-77-s01" {
		t.Fatalf("deleted %v", deleted)
	}
	sh, _ := e.s.Get(77)
	if !slices.Contains(sh.Blocked, "Tiny.Show.S01.1080p.WEB-DL.x264-GRP") {
		t.Errorf("not blocked: %v", sh.Blocked)
	}
	// The search again runs in the background and must not pick the pack.
	for i := 0; i < 100; i++ {
		e.qb.mu.Lock()
		n := len(e.qb.added)
		e.qb.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Wait for the background search to save its note (it writes the data
	// file; the test's folder is removed when the test ends).
	for i := 0; i < 500; i++ {
		if sh, _ := e.s.Get(77); sh.LastSearch != nil && strings.Contains(sh.LastSearch.Message, "seeders. ") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.qb.mu.Lock()
	defer e.qb.mu.Unlock()
	if len(e.qb.added) == 0 || slices.Contains(e.qb.added, "oneforarr-show-77-s01") {
		t.Errorf("after giving up, sent %v", e.qb.added)
	}
}

// Cancelling removes the torrent and stops following those episodes.
func TestCancel(t *testing.T) {
	e := newEnv(t, packAnswer)
	ctx := context.Background()
	if _, err := e.s.Add(ctx, 77, "1080p", MonitorAll); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SearchAndGrab(ctx, 77, Target{Season: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Cancel(ctx, 77, "oneforarr-show-77-s01"); err != nil {
		t.Fatal(err)
	}
	sh, _ := e.s.Get(77)
	if len(sh.Downloads) != 0 || len(e.qb.deleted) != 1 {
		t.Errorf("downloads %v, deleted %v", sh.Downloads, e.qb.deleted)
	}
	for _, ep := range sh.Episodes {
		if ep.Season == 1 && ep.Monitored {
			t.Errorf("S01E%02d still followed", ep.Number)
		}
	}
	if len(e.s.Wanted(sh)) != 0 {
		t.Error("cancelled season still wanted")
	}
}

// The daily refresh must finish (it once froze the whole app by reading
// the data while it was locked) and pick up files dropped in the folder.
func TestRefreshFinishes(t *testing.T) {
	e := newEnv(t, packAnswer)
	ctx := context.Background()
	if _, err := e.s.Add(ctx, 77, "1080p", MonitorAll); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.s.Refresh(ctx, 77) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Refresh froze")
	}
	if sh, _ := e.s.Get(77); sh.RefreshError != "" {
		t.Errorf("refresh error: %s", sh.RefreshError)
	}
}

// Removing a show cancels its downloads in qBittorrent first.
func TestRemoveCancelsDownloads(t *testing.T) {
	e := newEnv(t, packAnswer)
	ctx := context.Background()
	if _, err := e.s.Add(ctx, 77, "1080p", MonitorAll); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SearchAndGrab(ctx, 77, Target{Season: 1}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.Remove(ctx, 77, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.s.Get(77); ok || !slices.Equal(e.qb.deleted, []string{"h-oneforarr-show-77-s01"}) {
		t.Errorf("still there: %v, deleted %v", ok, e.qb.deleted)
	}
}
