package shows

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/indexers"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/metadata"
)

const gb = 1 << 30

var show = store.Show{TMDBID: 1, Title: "The Office", Year: 2005, Quality: "1080p"}
var rules = store.ShowRules{Quality: "1080p", MaxEpisodeGB: 4, MinSeeders: 1}

func rel(title string, size int64, seeders int) model.Release {
	return model.Release{Title: title, Size: size, Seeders: seeders, DownloadURL: "http://x.invalid/" + title}
}

func reasonsByTitle(ds []Decision) map[string]string {
	out := map[string]string{}
	for _, d := range ds {
		out[d.Release.Title] = strings.Join(d.Reasons, "; ")
	}
	return out
}

func TestDecideEpisode(t *testing.T) {
	ds := Decide(show, Target{Season: 2, Episode: 3}, 1, rules, []model.Release{
		rel("The.Office.US.S02E03.1080p.WEB.h264", 1*gb, 3), // a country after the name is fine
		rel("The.Officer.S02E03.1080p.WEB.h264", 1*gb, 10),
		rel("The.Office.S02E03.1080p.WEB.h264-GRP", 1*gb, 10),
		rel("The.Office.S02E03.720p.HDTV.x264", 500<<20, 50),
		rel("The.Office.S02E04.1080p.WEB.h264", 1*gb, 10),
		rel("The.Office.S03E03.1080p.WEB.h264", 1*gb, 10),
		rel("The.Office.S02.1080p.BluRay.x264", 20*gb, 10),
		rel("The.Office.S02E03.2160p.WEB.h265", 3*gb, 10),
		rel("The.Office.S02E03.1080p.WEB.h264.REMUX", 9*gb, 10),
	})
	if !ds[0].Accepted || ds[0].Release.Title != "The.Office.S02E03.1080p.WEB.h264-GRP" {
		t.Fatalf("best: %+v", ds[0])
	}
	if !ds[1].Accepted || ds[1].Release.Title != "The.Office.US.S02E03.1080p.WEB.h264" {
		t.Errorf("second: %+v", ds[1].Release.Title)
	}
	r := reasonsByTitle(ds)
	for title, want := range map[string]string{
		"The.Officer.S02E03.1080p.WEB.h264":      "Different show",
		"The.Office.S02E04.1080p.WEB.h264":       "Different episode (E04)",
		"The.Office.S03E03.1080p.WEB.h264":       "Different season (S03)",
		"The.Office.S02.1080p.BluRay.x264":       "A whole season, not one episode",
		"The.Office.S02E03.2160p.WEB.h265":       "2160p isn't in",
		"The.Office.S02E03.1080p.WEB.h264.REMUX": "Too big",
	} {
		if !strings.Contains(r[title], want) {
			t.Errorf("%s: %q, want %q", title, r[title], want)
		}
	}
}

func TestDecideSeasonPack(t *testing.T) {
	ds := Decide(show, Target{Season: 2}, 6, rules, []model.Release{
		rel("The.Office.S02.1080p.BluRay.x264-GRP", 20*gb, 10), // 6 episodes × 4 GB = 24 GB limit
		rel("The.Office.S02E01.1080p.WEB.h264", 1*gb, 10),
		rel("The.Office.S02.2160p.BluRay.REMUX", 90*gb, 10),
	})
	if !ds[0].Accepted || ds[0].Release.Title != "The.Office.S02.1080p.BluRay.x264-GRP" {
		t.Fatalf("best: %+v", ds[0])
	}
	r := reasonsByTitle(ds)
	if !strings.Contains(r["The.Office.S02E01.1080p.WEB.h264"], "A single episode") {
		t.Errorf("single episode: %q", r["The.Office.S02E01.1080p.WEB.h264"])
	}
	if !strings.Contains(r["The.Office.S02.2160p.BluRay.REMUX"], "limit 24 GB") {
		t.Errorf("pack limit: %q", r["The.Office.S02.2160p.BluRay.REMUX"])
	}
}

func TestMergeAndMonitor(t *testing.T) {
	fresh := []metadata.Episode{
		{Season: 1, Number: 1, AirDate: "2020-01-01"}, {Season: 1, Number: 2, AirDate: "2020-01-08"},
		{Season: 2, Number: 1, AirDate: "2026-12-01"},
	}
	today := "2026-11-18"
	count := func(eps []store.Episode) (n int) {
		for _, e := range eps {
			if e.Monitored {
				n++
			}
		}
		return
	}
	for monitor, want := range map[string]int{MonitorAll: 3, MonitorFuture: 1, MonitorLatest: 1, MonitorNone: 0} {
		eps := merge(store.Show{Monitor: monitor}, nil, fresh, today)
		if got := count(eps); got != want {
			t.Errorf("%s: %d followed, want %d", monitor, got, want)
		}
	}
	// Refresh keeps choices and files, adds new episodes, drops vanished
	// ones without files.
	old := merge(store.Show{Monitor: MonitorLatest}, nil, fresh, today)
	old[0].File = &store.MediaFile{Path: "/x"}
	old[0].Monitored = true
	fresh2 := []metadata.Episode{{Season: 1, Number: 1, Title: "Renamed"}, {Season: 2, Number: 1}, {Season: 2, Number: 2}, {Season: 3, Number: 1}}
	eps := merge(store.Show{Monitor: MonitorLatest}, old, fresh2, today)
	if len(eps) != 4 || eps[0].Title != "Renamed" || eps[0].File == nil || !eps[0].Monitored {
		t.Fatalf("kept: %+v", eps)
	}
	if !eps[2].Monitored || !eps[3].Monitored {
		t.Errorf("new episodes in the latest season and after should be followed: %+v", eps)
	}
}

func TestEpisodePath(t *testing.T) {
	got := EpisodePath("/tv", store.Show{Title: "Mr. Robot", Year: 2015}, store.Episode{Season: 1, Number: 2, Title: "eps1.1_ones-and-zer0es.mpeg"}, "WEBDL-1080p", ".mkv")
	want := "/tv/Mr. Robot (2015)/Season 01/Mr. Robot (2015) - S01E02 - eps1.1_ones-and-zer0es.mpeg WEBDL-1080p.mkv"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// ---- the whole way: add, search (season pack), send, follow, import ----

type fakeQBit struct {
	mu      sync.Mutex
	added   []string
	content string
	name    string
	done    bool
	state   string // default stalledUP
	deleted []string
}

func (f *fakeQBit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/v2/auth/login":
		w.WriteHeader(http.StatusNoContent)
	case "/api/v2/torrents/createCategory":
		w.WriteHeader(http.StatusConflict)
	case "/api/v2/torrents/add":
		_ = r.ParseForm()
		f.added = append(f.added, r.Form.Get("tags"))
		w.Write([]byte("Ok."))
	case "/api/v2/torrents/info":
		var list []map[string]any
		for _, tag := range f.added {
			p := 0.3
			if f.done {
				p = 1
			}
			state := f.state
			if state == "" {
				state = "stalledUP"
			}
			list = append(list, map[string]any{"hash": "h-" + tag, "name": f.name, "progress": p, "state": state,
				"content_path": f.content, "tags": tag})
		}
		json.NewEncoder(w).Encode(list)
	case "/api/v2/torrents/delete":
		_ = r.ParseForm()
		hash := r.Form.Get("hashes")
		f.deleted = append(f.deleted, hash)
		f.added = slices.DeleteFunc(f.added, func(tag string) bool { return "h-"+tag == hash })
	default:
		http.NotFound(w, r)
	}
}

const siteDef = `
id: testsite
name: Test Site
type: public
links: [https://x.invalid/]
caps:
  categorymappings: [{id: t, cat: TV, desc: TV}]
  modes: {search: [q], tv-search: [q, season, ep]}
search:
  paths: [{path: api, response: {type: json}}]
  inputs: {q: "{{ .Keywords }}"}
  rows: {selector: r}
  fields:
    title: {selector: t}
    category: {text: t}
    download: {selector: l}
    seeders: {selector: s}
    size: {selector: z}
`

// env is a test setup: fake TMDB/TVmaze, one indexer answering siteAnswer,
// a fake qBittorrent, and a media folder with a finished season pack.
type env struct {
	s       *Service
	qb      *fakeQBit
	media   string
	pack    string
	queries *[]string
}

func newEnv(t *testing.T, siteAnswer string) env {
	dir := t.TempDir()
	media := filepath.Join(dir, "media")
	pack := filepath.Join(media, "downloads", "Tiny.Show.S01.1080p.WEB-DL.x264-GRP")
	os.MkdirAll(pack, 0o755)
	for _, n := range []string{"Tiny.Show.S01E01.1080p.WEB-DL.mkv", "Tiny.Show.S01E02.1080p.WEB-DL.mkv", "Tiny.Show.S01E03.1080p.WEB-DL.mkv", "sample.mkv"} {
		os.WriteFile(filepath.Join(pack, n), []byte(strings.Repeat("x", 1000)), 0o644)
	}
	os.MkdirAll(filepath.Join(media, "tv"), 0o755)

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/tv/77"): // TMDB
			w.Write([]byte(`{"id":77,"name":"Tiny Show","first_air_date":"2020-01-01","status":"Ended","external_ids":{"tvdb_id":555}}`))
		case r.URL.Path == "/lookup/shows": // TVmaze
			w.Write([]byte(`{"id":9}`))
		case r.URL.Path == "/shows/9/episodes":
			w.Write([]byte(`[{"season":1,"number":1,"name":"One","airdate":"2020-01-01"},
				{"season":1,"number":2,"name":"Two","airdate":"2020-01-08"},
				{"season":1,"number":3,"name":"Three: The End","airdate":"2020-01-15"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	metadata.TMDBBase, metadata.TVmazeBase = api.URL, api.URL

	var queries []string
	var qmu sync.Mutex
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qmu.Lock()
		queries = append(queries, r.URL.Query().Get("q"))
		qmu.Unlock()
		w.Write([]byte(siteAnswer))
	}))
	t.Cleanup(site.Close)

	qb := &fakeQBit{content: "/data/downloads/Tiny.Show.S01.1080p.WEB-DL.x264-GRP", name: "Tiny.Show.S01.1080p.WEB-DL.x264-GRP"}
	qsrv := httptest.NewServer(qb)
	t.Cleanup(qsrv.Close)

	db, _ := store.Open(filepath.Join(dir, "data.json"))
	_ = db.Update(func(d *store.Data) error {
		d.Settings.Metadata.TMDBKey = "k"
		d.Settings.QBittorrent.URL = qsrv.URL
		d.Settings.Folders = store.Folders{Movies: filepath.Join(media, "movies"), Shows: filepath.Join(media, "tv"),
			Downloads: filepath.Join(media, "downloads"), QBitRoot: "/data", AppRoot: media}
		return nil
	})
	defs := filepath.Join(dir, "defs")
	os.MkdirAll(defs, 0o755)
	os.WriteFile(filepath.Join(defs, "testsite.yml"), []byte(siteDef), 0o644)
	idx := indexers.New(db, defs)
	if _, err := idx.Add(indexers.Input{Kind: indexers.KindDefinition, Definition: "testsite", Enabled: true, URL: site.URL}); err != nil {
		t.Fatal(err)
	}
	return env{s: New(db, idx), qb: qb, media: media, pack: pack, queries: &queries}
}

const packAnswer = `{"r":[
	{"t":"Tiny.Show.S01.1080p.WEB-DL.x264-GRP","l":"magnet:?xt=urn:btih:aa&tr=x","s":"20","z":"3 GB"},
	{"t":"Tiny.Show.S01E01.1080p.WEB-DL.x264-GRP","l":"magnet:?xt=urn:btih:bb&tr=x","s":"20","z":"1 GB"}]}`

func TestAddSearchImportSeasonPack(t *testing.T) {
	e := newEnv(t, packAnswer)
	s, qb, media, pack, queries := e.s, e.qb, e.media, e.pack, *e.queries
	ctx := context.Background()

	sh, err := s.Add(ctx, 77, "1080p", MonitorAll)
	if err != nil {
		t.Fatal(err)
	}
	if sh.TVmazeID != 9 || len(sh.Episodes) != 3 || !sh.Episodes[2].Monitored {
		t.Fatalf("added: %+v", sh)
	}

	msg, err := s.SearchAndGrab(ctx, 77, Target{})
	if err != nil || msg != "Sent 1 season pack to qBittorrent." {
		t.Fatalf("search: %q %v (queries %v)", msg, err, queries)
	}
	queries = *e.queries
	if queries[0] != "Tiny Show S01" || len(qb.added) != 1 || qb.added[0] != "oneforarr-show-77-s01" {
		t.Fatalf("queries %v, added %v", queries, qb.added)
	}
	// Nothing more to search while the pack downloads.
	if msg, _ := s.SearchAndGrab(ctx, 77, Target{}); !strings.HasPrefix(msg, "Nothing missing") {
		t.Errorf("second search: %q", msg)
	}

	qb.done = true
	if err := s.CheckDownloads(ctx); err != nil {
		t.Fatal(err)
	}
	sh, _ = s.Get(77)
	if len(sh.Downloads) != 0 {
		t.Fatalf("download not cleared: %+v (note %+v)", sh.Downloads, sh.LastSearch)
	}
	want := filepath.Join(media, "tv", "Tiny Show (2020)", "Season 01", "Tiny Show (2020) - S01E03 - Three - The End WEBDL-1080p.mkv")
	if sh.Episodes[2].File == nil || sh.Episodes[2].File.Path != want {
		t.Fatalf("episode 3 file: %+v", sh.Episodes[2].File)
	}
	a, _ := os.Stat(want)
	b, _ := os.Stat(filepath.Join(pack, "Tiny.Show.S01E03.1080p.WEB-DL.mkv"))
	if !os.SameFile(a, b) {
		t.Error("not a hardlink")
	}
	for _, e := range sh.Episodes {
		if e.File == nil {
			t.Errorf("S%02dE%02d not imported", e.Season, e.Number)
		}
	}

	// Removing the show and re-adding it finds the files already there.
	_ = s.Remove(77, false)
	sh, _ = s.Add(ctx, 77, "1080p", MonitorAll)
	for _, e := range sh.Episodes {
		if e.File == nil {
			t.Errorf("existing S%02dE%02d not found", e.Season, e.Number)
		}
	}
}

func TestAired(t *testing.T) {
	now := time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC)
	cases := []struct {
		e    store.Episode
		want bool
	}{
		{store.Episode{AirDate: "2026-11-18"}, true},
		{store.Episode{AirDate: "2026-11-19"}, false},
		{store.Episode{AirDate: ""}, false},
		{store.Episode{AirDate: "2026-11-18", AirStamp: now.Add(time.Hour)}, false},
	}
	for _, c := range cases {
		if got := Aired(c.e, now); got != c.want {
			t.Errorf("Aired(%+v) = %v", c.e, got)
		}
	}
}

// Only single episodes on offer: "Search season" finds no pack, then
// searches each episode, in parallel. Clicking two episodes at once works;
// clicking the same one twice doesn't search twice.
func TestSeasonFallsBackToEpisodesInParallel(t *testing.T) {
	e := newEnv(t, `{"r":[
		{"t":"Tiny.Show.S01E01.1080p.WEB-DL.x264-GRP","l":"magnet:?xt=urn:btih:e1&tr=x","s":"20","z":"1 GB"},
		{"t":"Tiny.Show.S01E02.1080p.WEB-DL.x264-GRP","l":"magnet:?xt=urn:btih:e2&tr=x","s":"20","z":"1 GB"},
		{"t":"Tiny.Show.S01E03.1080p.WEB-DL.x264-GRP","l":"magnet:?xt=urn:btih:e3&tr=x","s":"20","z":"1 GB"}]}`)
	ctx := context.Background()
	if _, err := e.s.Add(ctx, 77, "1080p", MonitorAll); err != nil {
		t.Fatal(err)
	}

	// Two different episodes at the same time.
	var wg sync.WaitGroup
	msgs := make([]string, 2)
	for i, ep := range []int{1, 2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			msgs[i], _ = e.s.SearchAndGrab(ctx, 77, Target{Season: 1, Episode: ep})
		}()
	}
	wg.Wait()
	for i, m := range msgs {
		if !strings.HasPrefix(m, "Sent Tiny.Show.S01E0") {
			t.Errorf("episode %d: %q", i+1, m)
		}
	}

	// The season: no pack, so the one episode left is searched on its own.
	msg, err := e.s.SearchAndGrab(ctx, 77, Target{Season: 1})
	if err != nil || !strings.Contains(msg, "no season pack") || !strings.Contains(msg, "Sent 1 episode") {
		t.Fatalf("season: %q %v", msg, err)
	}
	if len(e.qb.added) != 3 {
		t.Errorf("sent %v", e.qb.added)
	}

	// Everything is downloading now: nothing is sent twice.
	if msg, _ := e.s.SearchAndGrab(ctx, 77, Target{Season: 1, Episode: 2}); !strings.Contains(msg, "already downloading") {
		t.Errorf("repeat: %q", msg)
	}
	if len(e.qb.added) != 3 {
		t.Errorf("sent twice: %v", e.qb.added)
	}
}
