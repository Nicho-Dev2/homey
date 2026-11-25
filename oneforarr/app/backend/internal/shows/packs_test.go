package shows

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
)

func TestPackWorthIt(t *testing.T) {
	s := &Service{Now: func() time.Time { return time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC) }}
	file := &store.MediaFile{Path: "/tv/x.mkv", Quality: "WEBDL-1080p", Resolution: 1080}
	season := func(n int) store.Show {
		sh := store.Show{}
		for i := 1; i <= n; i++ {
			sh.Episodes = append(sh.Episodes, store.Episode{Season: 1, Number: i, AirDate: "2015-01-01", Monitored: true})
		}
		return sh
	}
	single := func(ep int, progress float64, stuck bool) store.ShowDownload {
		d := store.ShowDownload{Season: 1, Episode: ep}
		d.Tag, d.Progress = Tag(store.Show{}, Target{Season: 1, Episode: ep}), progress
		if stuck {
			d.StuckSince = time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
		}
		return d
	}

	cases := []struct {
		name string
		edit func(*store.Show)
		want bool
	}{
		{"all 5 missing", func(*store.Show) {}, true},
		{"3 missing, 2 in the library", func(sh *store.Show) { sh.Episodes[0].File, sh.Episodes[1].File = file, file }, true},
		{"2 missing", func(sh *store.Show) { sh.Episodes[0].File, sh.Episodes[1].File, sh.Episodes[2].File = file, file, file }, false},
		{"unfollowed episodes don't count", func(sh *store.Show) {
			sh.Episodes[0].Monitored, sh.Episodes[1].Monitored, sh.Episodes[2].Monitored = false, false, false
		}, false},
		{"stuck singles count as missing", func(sh *store.Show) {
			sh.Episodes[0].File, sh.Episodes[1].File = file, file
			sh.Downloads = []store.ShowDownload{single(3, 0.9, true), single(4, 0.1, false)}
		}, true},
		{"singles well along don't", func(sh *store.Show) {
			sh.Episodes[0].File, sh.Episodes[1].File = file, file
			sh.Downloads = []store.ShowDownload{single(3, 0.6, false)}
		}, false},
		{"a pack is already downloading", func(sh *store.Show) {
			sh.Downloads = []store.ShowDownload{{Season: 1}}
		}, false},
		{"not every episode has aired", func(sh *store.Show) { sh.Episodes[4].AirDate = "2027-01-01" }, false},
	}
	for _, c := range cases {
		sh := season(5)
		c.edit(&sh)
		if got := s.packWorthIt(sh, 1); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

const packAndSingles = `{"r":[
	{"t":"Tiny.Show.S01.1080p.WEB-DL.x264-GRP","l":"magnet:?xt=urn:btih:aa&tr=x","s":"20","z":"3 GB"},
	{"t":"Tiny.Show.S01E01.1080p.WEB-DL.x264-GRP","l":"magnet:?xt=urn:btih:e1&tr=x","s":"20","z":"1 GB"},
	{"t":"Tiny.Show.S01E02.1080p.WEB-DL.x264-GRP","l":"magnet:?xt=urn:btih:e2&tr=x","s":"20","z":"1 GB"}]}`

// Sending a season pack cancels the season's weak single downloads, keeps
// the ones well along, and leaves the episodes followed.
func TestPackDropsWeakSingles(t *testing.T) {
	e := newEnv(t, packAndSingles)
	ctx := context.Background()
	if _, err := e.s.Add(ctx, 77, "1080p", MonitorAll); err != nil {
		t.Fatal(err)
	}
	for _, ep := range []int{1, 2} {
		if msg, _ := e.s.SearchAndGrab(ctx, 77, Target{Season: 1, Episode: ep}); !strings.HasPrefix(msg, "Sent") {
			t.Fatalf("E%02d: %q", ep, msg)
		}
	}
	_ = e.s.update(77, func(x *store.Show) {
		for i := range x.Downloads {
			if x.Downloads[i].Episode == 2 {
				x.Downloads[i].Progress = 0.8
			}
		}
	})

	if msg, _ := e.s.SearchAndGrab(ctx, 77, Target{Season: 1}); !strings.HasPrefix(msg, "Sent season pack") {
		t.Fatalf("season: %q", msg)
	}
	if !slices.Equal(e.qb.deleted, []string{"h-oneforarr-show-77-s01e01"}) {
		t.Errorf("deleted %v", e.qb.deleted)
	}
	sh, _ := e.s.Get(77)
	var tags []string
	for _, d := range sh.Downloads {
		tags = append(tags, d.Tag)
	}
	slices.Sort(tags)
	if !slices.Equal(tags, []string{"oneforarr-show-77-s01", "oneforarr-show-77-s01e02"}) {
		t.Errorf("downloads %v", tags)
	}
	if !sh.Episodes[0].Monitored || slices.Contains(sh.Blocked, "Tiny.Show.S01E01.1080p.WEB-DL.x264-GRP") {
		t.Error("the cancelled single's episode should stay followed and its release unblocked")
	}
}

// A season pack fills the gaps and keeps the files already there.
func TestPackKeepsFilesAlreadyThere(t *testing.T) {
	e := newEnv(t, packAnswer)
	ctx := context.Background()
	if _, err := e.s.Add(ctx, 77, "1080p", MonitorAll); err != nil {
		t.Fatal(err)
	}
	have := filepath.Join(e.media, "tv", "Tiny Show (2020)", "Season 01", "mine.mkv")
	_ = e.s.update(77, func(x *store.Show) {
		x.Episodes[0].File = &store.MediaFile{Path: have, Quality: "WEBDL-1080p", Resolution: 1080}
	})
	if msg, _ := e.s.SearchAndGrab(ctx, 77, Target{Season: 1}); !strings.HasPrefix(msg, "Sent season pack") {
		t.Fatalf("season: %q", msg)
	}
	e.qb.mu.Lock()
	e.qb.done = true
	e.qb.mu.Unlock()
	if err := e.s.CheckDownloads(ctx); err != nil {
		t.Fatal(err)
	}
	sh, _ := e.s.Get(77)
	if sh.Episodes[0].File.Path != have {
		t.Errorf("E01 replaced: %s", sh.Episodes[0].File.Path)
	}
	if sh.Episodes[1].File == nil || sh.Episodes[2].File == nil {
		t.Error("E02 and E03 not imported")
	}
	if len(sh.Downloads) != 0 || !strings.Contains(sh.LastSearch.Message, "Kept the files already there for 1 episode") {
		t.Errorf("downloads %v, note %q", sh.Downloads, sh.LastSearch.Message)
	}
}

// When a single episode dies and the season is now missing enough, the
// search again goes for the season pack.
func TestDeadSingleTurnsIntoPack(t *testing.T) {
	e := newEnv(t, packAnswer)
	ctx := context.Background()
	if _, err := e.s.Add(ctx, 77, "1080p", MonitorAll); err != nil {
		t.Fatal(err)
	}
	if msg, _ := e.s.SearchAndGrab(ctx, 77, Target{Season: 1, Episode: 1}); !strings.HasPrefix(msg, "Sent") {
		t.Fatalf("E01: %q", msg)
	}
	e.qb.mu.Lock()
	e.qb.state = "stalledDL"
	e.qb.mu.Unlock()
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	e.s.Now = func() time.Time { return now }
	_ = e.s.CheckDownloads(ctx) // starts the clock
	now = now.Add(6 * time.Hour)
	_ = e.s.CheckDownloads(ctx)

	// The search again runs in the background; wait for its note.
	for i := 0; i < 500; i++ {
		if sh, _ := e.s.Get(77); sh.LastSearch != nil && strings.Contains(sh.LastSearch.Message, "seeders. ") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.qb.mu.Lock()
	defer e.qb.mu.Unlock()
	if !slices.Contains(e.qb.added, "oneforarr-show-77-s01") {
		t.Errorf("after giving up on E01, sent %v", e.qb.added)
	}
}

// A finished download whose sound is out of sync is deleted, blocked and
// searched for again, and nothing of it is imported.
func TestOutOfSyncDownloadRejected(t *testing.T) {
	e := newEnv(t, packAnswer)
	ctx := context.Background()
	drift, err := os.ReadFile(filepath.Join("..", "mediacheck", "testdata", "drift.mkv"))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(e.pack, "Tiny.Show.S01E02.1080p.WEB-DL.mkv"), drift, 0o644)
	if _, err := e.s.Add(ctx, 77, "1080p", MonitorAll); err != nil {
		t.Fatal(err)
	}
	if msg, _ := e.s.SearchAndGrab(ctx, 77, Target{Season: 1}); !strings.HasPrefix(msg, "Sent season pack") {
		t.Fatalf("season: %q", msg)
	}
	e.qb.mu.Lock()
	e.qb.done = true
	e.qb.mu.Unlock()
	if err := e.s.CheckDownloads(ctx); err != nil {
		t.Fatal(err)
	}

	// The search again runs in the background; wait for its result.
	var sh store.Show
	for i := 0; i < 500; i++ {
		sh, _ = e.s.Get(77)
		if note := sh.LastSearch; note != nil && strings.Contains(note.Message, "Season 1:") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, ep := range sh.Episodes {
		if ep.File != nil {
			t.Errorf("E%02d imported from an out-of-sync pack", ep.Number)
		}
	}
	e.qb.mu.Lock()
	deleted := slices.Clone(e.qb.deleted)
	e.qb.mu.Unlock()
	if !slices.Contains(deleted, "h-oneforarr-show-77-s01") || !slices.Contains(sh.Blocked, "Tiny.Show.S01.1080p.WEB-DL.x264-GRP") {
		t.Errorf("deleted %v, blocked %v", deleted, sh.Blocked)
	}
	if !strings.Contains(sh.LastSearch.Message, "out of sync (Tiny.Show.S01E02.1080p.WEB-DL.mkv: its sound is 4.9 s shorter than its picture)") {
		t.Errorf("note %q", sh.LastSearch.Message)
	}
}

// The newest releases are checked for a REPACK of the release an episode's
// file came from; it's sent to replace the file.
func TestRepackReplacesFile(t *testing.T) {
	e := newEnv(t, packAnswer)
	ctx := context.Background()
	if _, err := e.s.Add(ctx, 77, "1080p", MonitorAll); err != nil {
		t.Fatal(err)
	}
	_ = e.s.update(77, func(x *store.Show) {
		x.Episodes[0].File = &store.MediaFile{Path: "/tv/e1.mkv", Quality: "WEBDL-1080p", Resolution: 1080, Release: "Tiny.Show.S01E01.1080p.WEB.h264-GRP"}
		x.Episodes[1].File = &store.MediaFile{Path: "/tv/e2.mkv", Quality: "WEBDL-1080p", Resolution: 1080} // release unknown
		x.Episodes[2].Monitored = false
	})
	rel := func(title string) model.Release {
		return model.Release{Title: title, MagnetURL: "magnet:?xt=urn:btih:" + title, Seeders: 20, Size: 1 << 30, IndexerName: "Test"}
	}
	sent := e.s.MatchNewReleases(ctx, []model.Release{
		rel("Tiny.Show.S01E01.REPACK.1080p.WEB.h264-OTHER"), // another group's repack: no
		rel("Tiny.Show.S01E02.REPACK.1080p.WEB.h264-GRP"),   // the file's release isn't known: no
		rel("Tiny.Show.S01E01.REPACK.1080p.WEB.h264-GRP"),
	})
	e.qb.mu.Lock()
	defer e.qb.mu.Unlock()
	if sent != 1 || !slices.Equal(e.qb.added, []string{"oneforarr-show-77-s01e01"}) {
		t.Errorf("sent %d: %v", sent, e.qb.added)
	}
}
