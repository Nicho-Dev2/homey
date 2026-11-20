package watchdog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
)

// fakeQB answers like qBittorrent with a fixed DHT count and counts
// shutdowns.
type fakeQB struct {
	dht       int
	status    string
	shutdowns int
}

func (f *fakeQB) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v2/auth/login":
		w.WriteHeader(http.StatusNoContent)
	case "/api/v2/transfer/info":
		w.Write([]byte(`{"dht_nodes":` + strconv.Itoa(f.dht) + `,"connection_status":"` + f.status + `","dl_info_speed":0}`))
	case "/api/v2/app/preferences":
		w.Write([]byte(`{"dht":true}`))
	case "/api/v2/app/shutdown":
		f.shutdowns++
	default:
		http.NotFound(w, r)
	}
}

func TestRestartsOnlyAfterFiveStuckMinutes(t *testing.T) {
	qb := &fakeQB{dht: 0, status: "firewalled"}
	srv := httptest.NewServer(qb)
	defer srv.Close()

	db, _ := store.Open(filepath.Join(t.TempDir(), "data.json"))
	_ = db.Update(func(d *store.Data) error {
		d.Settings.QBittorrent.URL = srv.URL
		d.Movies = []store.Movie{{TMDBID: 1, Download: &store.Download{Tag: "x"}}}
		return nil
	})
	w := New(db)
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	w.Now = func() time.Time { return now }
	ctx := context.Background()

	for i := 0; i < 10; i++ { // 4.5 minutes of checks
		w.Check(ctx)
		now = now.Add(30 * time.Second)
	}
	if qb.shutdowns != 0 || w.Health().Problem == "" {
		t.Fatalf("restarted too soon (%d) or no problem noted: %+v", qb.shutdowns, w.Health())
	}
	w.Check(ctx) // 5 minutes
	if qb.shutdowns != 1 || w.Health().Restarted.IsZero() {
		t.Fatalf("not restarted after 5 minutes: %d", qb.shutdowns)
	}
	// Still stuck: no second restart within 15 minutes.
	for i := 0; i < 20; i++ {
		now = now.Add(30 * time.Second)
		w.Check(ctx)
	}
	if qb.shutdowns != 1 {
		t.Errorf("restarted again within 15 minutes: %d", qb.shutdowns)
	}

	// Healthy again: the problem clears.
	qb.dht = 9
	w.Check(ctx)
	if w.Health().Problem != "" || w.Health().DHTNodes != 9 {
		t.Errorf("health: %+v", w.Health())
	}
}

func TestNoRestartWhenIdleOrTurnedOff(t *testing.T) {
	qb := &fakeQB{dht: 0, status: "firewalled"}
	srv := httptest.NewServer(qb)
	defer srv.Close()
	db, _ := store.Open(filepath.Join(t.TempDir(), "data.json"))
	_ = db.Update(func(d *store.Data) error {
		d.Settings.QBittorrent.URL = srv.URL
		return nil
	})
	w := New(db)
	now := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	w.Now = func() time.Time { return now }
	for i := 0; i < 20; i++ {
		w.Check(context.Background())
		now = now.Add(30 * time.Second)
	}
	if qb.shutdowns != 0 {
		t.Error("restarted with nothing downloading")
	}

	// Disconnected counts even when idle, but the setting is off.
	qb.status = "disconnected"
	_ = db.Update(func(d *store.Data) error { d.Settings.QBittorrent.RestartWhenStuck = false; return nil })
	for i := 0; i < 20; i++ {
		w.Check(context.Background())
		now = now.Add(30 * time.Second)
	}
	if qb.shutdowns != 0 || w.Health().Problem == "" {
		t.Errorf("setting off: shutdowns %d, health %+v", qb.shutdowns, w.Health())
	}
}
