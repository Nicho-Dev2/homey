// Package schedule does the app's automatic work, every 30 seconds:
//
//   - follow downloads and import finished ones (movies and shows);
//   - update episode lists once a day;
//   - check the indexers' newest releases (RSS) every few minutes;
//   - search again for each wanted movie and show every few hours;
//   - tell Jellyfin to scan after imports;
//   - restart qBittorrent when it's stuck without a connection (watchdog).
//
// Searches can take minutes, so they run in the background, one batch at a
// time; following downloads never waits for them.
package schedule

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/indexers"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/jellyfin"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/movies"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/shows"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/watchdog"
)

// Every is how often Tick runs.
const Every = 30 * time.Second

// jellyfinGap is the least time between two Jellyfin scans.
const jellyfinGap = time.Minute

// Scheduler holds the timing state of the automatic work.
type Scheduler struct {
	Store    *store.Store
	Indexers *indexers.Service
	Movies   *movies.Service
	Shows    *shows.Service
	Watchdog *watchdog.Watchdog // optional
	Now      func() time.Time

	searching atomic.Bool // a background batch is running

	mu            sync.Mutex
	lastRSS       time.Time
	scanWanted    bool
	lastScan      time.Time
	lastScanError string
}

// New returns the scheduler and connects the imports to Jellyfin scans.
func New(s *store.Store, idx *indexers.Service, mov *movies.Service, tv *shows.Service) *Scheduler {
	sc := &Scheduler{Store: s, Indexers: idx, Movies: mov, Shows: tv, Now: time.Now}
	mov.OnImport = sc.ImportDone
	tv.OnImport = sc.ImportDone
	return sc
}

// Run calls Tick every 30 seconds until ctx ends.
func (sc *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sc.Tick(ctx)
		}
	}
}

// Tick does one round of the automatic work.
func (sc *Scheduler) Tick(ctx context.Context) {
	sc.Movies.Tick(ctx)
	sc.Shows.Tick(ctx)
	sc.scanJellyfin(ctx)
	if sc.Watchdog != nil {
		sc.Watchdog.Check(ctx)
	}
	if sc.searching.CompareAndSwap(false, true) {
		go func() {
			defer sc.searching.Store(false)
			sc.searchBatch(ctx)
		}()
	}
}

// searchBatch checks new releases if due, then searches for one movie and
// one show if due.
func (sc *Scheduler) searchBatch(ctx context.Context) {
	set := sc.Store.Read().Settings.Schedule
	if set.RSSMinutes > 0 {
		sc.mu.Lock()
		due := sc.Now().Sub(sc.lastRSS) >= time.Duration(set.RSSMinutes)*time.Minute
		if due {
			sc.lastRSS = sc.Now()
		}
		sc.mu.Unlock()
		if due {
			sc.CheckNewReleases(ctx)
		}
	}
	if set.SearchHours > 0 {
		every := time.Duration(set.SearchHours) * time.Hour
		sc.Movies.AutoSearchNext(ctx, every)
		sc.Shows.AutoSearchNext(ctx, every)
	}
}

// CheckNewReleases asks every indexer for its newest releases and sends
// any that a wanted movie or episode needs.
func (sc *Scheduler) CheckNewReleases(ctx context.Context) {
	if !sc.anythingWanted() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res := sc.Indexers.Search(ctx, model.Query{Kind: "search"})
	for _, f := range res.Failures {
		log.Printf("schedule: newest releases from %s: %s", f.IndexerName, f.Error)
	}
	m := sc.Movies.MatchNewReleases(ctx, res.Releases)
	s := sc.Shows.MatchNewReleases(ctx, res.Releases)
	if m+s > 0 {
		log.Printf("schedule: %d newest releases checked; sent %d for movies and %d for shows", len(res.Releases), m, s)
	}
}

func (sc *Scheduler) anythingWanted() bool {
	d := sc.Store.Read()
	if len(d.Indexers) == 0 {
		return false
	}
	for _, m := range d.Movies {
		if sc.Movies.Wanted(m) {
			return true
		}
	}
	for _, sh := range d.Shows {
		if len(sc.Shows.Wanted(sh)) > 0 {
			return true
		}
	}
	return false
}

// ImportDone is called after a file is put in the library.
func (sc *Scheduler) ImportDone() {
	sc.mu.Lock()
	sc.scanWanted = true
	sc.mu.Unlock()
}

// scanJellyfin asks Jellyfin to scan, at most once a minute, when files
// were imported since the last scan.
func (sc *Scheduler) scanJellyfin(ctx context.Context) {
	set := sc.Store.Read().Settings.Jellyfin
	sc.mu.Lock()
	due := sc.scanWanted && sc.Now().Sub(sc.lastScan) >= jellyfinGap && set.URL != ""
	if due {
		sc.scanWanted, sc.lastScan = false, sc.Now()
	}
	sc.mu.Unlock()
	if !due {
		return
	}
	c, err := jellyfin.New(set.URL, set.APIKey)
	if err == nil {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		err = c.Refresh(ctx)
	}
	if err != nil {
		log.Printf("schedule: asking Jellyfin to scan: %v", err)
	}
}
