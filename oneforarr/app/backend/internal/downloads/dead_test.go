package downloads

import (
	"testing"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
)

func TestDead(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	var d store.Download
	stalled := Torrent{State: "stalledDL"}

	NoteStuck(&d, stalled, now)
	if !d.StuckSince.Equal(now) {
		t.Fatalf("clock not started: %v", d.StuckSince)
	}
	NoteStuck(&d, stalled, now.Add(5*time.Hour))
	if Dead(d, 6, now.Add(5*time.Hour)) {
		t.Error("dead after 5 hours")
	}
	if !Dead(d, 6, now.Add(6*time.Hour)) || Dead(d, 0, now.Add(99*time.Hour)) {
		t.Error("6 hours / turned off")
	}

	// Any progress resets the clock.
	NoteStuck(&d, Torrent{State: "stalledDL", Downloaded: 1000}, now.Add(6*time.Hour))
	if !d.StuckSince.IsZero() {
		t.Error("progress didn't reset")
	}
	// Queued torrents aren't trying, so they never start the clock.
	NoteStuck(&d, Torrent{State: "queuedDL", Downloaded: 1000}, now.Add(7*time.Hour))
	if !d.StuckSince.IsZero() {
		t.Error("queued counted as stuck")
	}
	// Seeders connected but nothing yet: not dead.
	NoteStuck(&d, Torrent{State: "metaDL", Downloaded: 1000, NumSeeds: 2}, now.Add(8*time.Hour))
	if !d.StuckSince.IsZero() {
		t.Error("seeders didn't reset")
	}
}
