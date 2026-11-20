// Package watchdog keeps an eye on qBittorrent's connection.
//
// After the VPN reconnects, qBittorrent sometimes keeps running without any
// working connection: 0 DHT nodes, downloads stuck at "Downloading
// metadata" or "Stalled". Restarting qBittorrent fixes it. When that state
// lasts 5 minutes while the app has downloads going, the watchdog asks
// qBittorrent to quit; the linuxserver image starts it again by itself
// (the program, or the container through restart: unless-stopped).
//
// It waits 15 minutes after a restart before it would restart again, and
// does nothing while "Restart qBittorrent when it loses its connection" is
// off in Settings.
package watchdog

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/downloads"
)

const (
	stuckFor     = 5 * time.Minute  // how long it must look stuck
	restartPause = 15 * time.Minute // least time between two restarts
)

// Health is what the Activity page shows about qBittorrent.
type Health struct {
	CheckedAt time.Time `json:"checkedAt,omitzero"`
	Reachable bool      `json:"reachable"`
	Error     string    `json:"error,omitempty"`    // why it couldn't be reached
	DHTNodes  int       `json:"dhtNodes"`           // -1 when DHT is off
	Status    string    `json:"status,omitempty"`   // connected, firewalled, disconnected
	Problem   string    `json:"problem,omitempty"`  // what looks wrong now, if anything
	BadSince  time.Time `json:"badSince,omitzero"`  // when it started
	Restarted time.Time `json:"restarted,omitzero"` // last restart by the watchdog or by hand
	Reason    string    `json:"reason,omitempty"`   // why it was restarted
}

// Watchdog holds what it has seen so far.
type Watchdog struct {
	Store *store.Store
	Now   func() time.Time

	mu     sync.Mutex
	health Health
}

// New returns a watchdog.
func New(s *store.Store) *Watchdog { return &Watchdog{Store: s, Now: time.Now} }

// Health returns the latest check.
func (w *Watchdog) Health() Health {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.health
}

// Check looks at qBittorrent once (the scheduler calls it every 30 s) and
// restarts it when it has been stuck long enough.
func (w *Watchdog) Check(ctx context.Context) {
	d := w.Store.Read()
	set := d.Settings
	active := activeDownloads(d)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	now := w.Now()
	h := w.Health()
	h.CheckedAt = now

	qb, err := downloads.Connect(ctx, set)
	var tr downloads.Transfer
	var dht bool
	if err == nil {
		tr, err = qb.Transfer(ctx)
	}
	if err == nil {
		dht, err = qb.DHTEnabled(ctx)
	}
	if err != nil {
		// Unreachable: nothing to restart through the API. Most often the
		// VPN container restarted and qBittorrent needs a container restart.
		h.Reachable, h.Error, h.Problem, h.BadSince = false, err.Error(), "", time.Time{}
		w.set(h)
		return
	}
	h.Reachable, h.Error, h.Status, h.DHTNodes = true, "", tr.Status, tr.DHTNodes
	if !dht {
		h.DHTNodes = -1
	}

	problem := ""
	switch {
	case tr.Status == "disconnected":
		problem = "qBittorrent has no connection"
	case dht && tr.DHTNodes == 0 && active && tr.DLSpeed == 0:
		problem = "qBittorrent has 0 DHT nodes and downloads aren't moving"
	}
	if problem == "" {
		h.Problem, h.BadSince = "", time.Time{}
		w.set(h)
		return
	}
	if h.BadSince.IsZero() {
		h.BadSince = now
		log.Printf("watchdog: %s; restarting it if this lasts %s", problem, stuckFor)
	}
	h.Problem = problem

	due := now.Sub(h.BadSince) >= stuckFor && now.Sub(h.Restarted) >= restartPause
	if due && set.QBittorrent.RestartWhenStuck {
		log.Printf("watchdog: %s for %s; restarting qBittorrent", problem, now.Sub(h.BadSince).Round(time.Second))
		if err := qb.Shutdown(ctx); err != nil {
			log.Printf("watchdog: restarting qBittorrent: %v", err)
		} else {
			h.Restarted, h.Reason = now, problem
			h.Problem, h.BadSince = "", time.Time{}
		}
	}
	w.set(h)
}

// RestartNow restarts qBittorrent right away (the button in Settings).
func (w *Watchdog) RestartNow(ctx context.Context) error {
	qb, err := downloads.Connect(ctx, w.Store.Read().Settings)
	if err != nil {
		return err
	}
	if err := qb.Shutdown(ctx); err != nil {
		return err
	}
	log.Printf("watchdog: qBittorrent restarted by hand")
	h := w.Health()
	h.Restarted, h.Reason, h.Problem, h.BadSince = w.Now(), "restarted by hand", "", time.Time{}
	w.set(h)
	return nil
}

func (w *Watchdog) set(h Health) {
	w.mu.Lock()
	w.health = h
	w.mu.Unlock()
}

// activeDownloads says whether the app is waiting on any download.
func activeDownloads(d store.Data) bool {
	for _, m := range d.Movies {
		if m.Download != nil {
			return true
		}
	}
	for _, sh := range d.Shows {
		if len(sh.Downloads) > 0 {
			return true
		}
	}
	return false
}
