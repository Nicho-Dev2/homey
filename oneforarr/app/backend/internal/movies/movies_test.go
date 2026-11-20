package movies

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/indexers"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/metadata"
)

var night = store.Movie{TMDBID: 10331, IMDBID: "tt0063350", Title: "Night of the Living Dead", Year: 1968, Quality: "1080p"}

var rules = store.MovieRules{Quality: "1080p", MaxSizeGB: 20, MinSeeders: 1}

func rel(title string, size int64, seeders int) model.Release {
	return model.Release{Title: title, Size: size, Seeders: seeders, DownloadURL: "http://x.invalid/" + title}
}

const gb = 1 << 30

func TestDecide(t *testing.T) {
	ds := Decide(night, rules, []model.Release{
		rel("Night.of.the.Living.Dead.1968.720p.BluRay.x264", 1*gb, 300),
		rel("Night.of.the.Living.Dead.1968.1080p.WEBRip.x264", 2*gb, 2),
		rel("Night.of.the.Living.Dead.1968.1080p.BluRay.x264", 2*gb, 40),
		rel("Night.of.the.Living.Dead.1968.2160p.UHD.BluRay.REMUX", 58*gb, 90),
		rel("Night.of.the.Living.Dead.1990.1080p.BluRay", 2*gb, 90),
		rel("Day.of.the.Dead.1985.1080p.BluRay", 2*gb, 90),
		rel("Night.of.the.Living.Dead.1968.HDCAM", 1*gb, 90),
		rel("Night of the Living Dead", 3*gb, 1),
		rel("Night.of.the.Living.Dead.1968.1080p.BluRay.sample", 20<<20, 9),
		rel("Night.of.the.Living.Dead.1968.1080p.WEB-DL", 2*gb, 0),
	})
	accepted := 0
	for _, d := range ds {
		if d.Accepted {
			accepted++
		}
	}
	if accepted != 3 {
		for _, d := range ds {
			t.Logf("%v %s %v", d.Accepted, d.Release.Title, d.Reasons)
		}
		t.Fatalf("accepted %d, want 3", accepted)
	}
	// 1080p with healthy seeders and Blu-ray wins over 1080p WEBRip with 2
	// seeders and over 720p with many.
	if got := ds[0].Release.Title; got != "Night.of.the.Living.Dead.1968.1080p.BluRay.x264" {
		t.Errorf("best = %s", got)
	}
	if ds[1].Release.Title != "Night.of.the.Living.Dead.1968.1080p.WEBRip.x264" {
		t.Errorf("second = %s", ds[1].Release.Title)
	}
	reasons := map[string]string{}
	for _, d := range ds {
		reasons[d.Release.Title] = strings.Join(d.Reasons, "; ")
	}
	want := map[string]string{
		"Night.of.the.Living.Dead.1968.2160p.UHD.BluRay.REMUX": "2160p isn't in",
		"Night.of.the.Living.Dead.1990.1080p.BluRay":           "Different year (1990)",
		"Day.of.the.Dead.1985.1080p.BluRay":                    "Different title",
		"Night.of.the.Living.Dead.1968.HDCAM":                  "Recorded in a cinema",
		"Night of the Living Dead":                             "No year",
		"Night.of.the.Living.Dead.1968.1080p.BluRay.sample":    "Too small",
		"Night.of.the.Living.Dead.1968.1080p.WEB-DL":           "Only 0 seeders",
	}
	for title, part := range want {
		if !strings.Contains(reasons[title], part) {
			t.Errorf("%s: reasons %q, want %q", title, reasons[title], part)
		}
	}
	if !strings.Contains(reasons["Night.of.the.Living.Dead.1968.2160p.UHD.BluRay.REMUX"], "Too big") {
		t.Error("58 GB remux not marked too big")
	}
}

func TestIMDbMatchSkipsTitleCheck(t *testing.T) {
	r := rel("Nuit des morts vivants 1080p BluRay", 2*gb, 10)
	r.IMDBID = "tt0063350"
	if ds := Decide(night, rules, []model.Release{r}); !ds[0].Accepted {
		t.Errorf("rejected: %v", ds[0].Reasons)
	}
}

func TestFolderName(t *testing.T) {
	cases := map[string]string{
		"Mission: Impossible": "Mission - Impossible (1996)",
		"AC/DC Live":          "AC-DC Live (1996)",
		"What?":               "What (1996)",
	}
	for title, want := range cases {
		if got := FolderName(store.Movie{Title: title, Year: 1996}); got != want {
			t.Errorf("FolderName(%q) = %q, want %q", title, got, want)
		}
	}
}

// ---- the whole way: add, search, send, follow, import ----

// fakeQBit records added torrents and reports them back, finished.
type fakeQBit struct {
	mu      sync.Mutex
	added   []string // tags
	content string   // content_path for the finished torrent
	done    bool
}

func (f *fakeQBit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/api/v2/auth/login":
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "x", Path: "/"})
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
			progress := 0.5
			if f.done {
				progress = 1
			}
			list = append(list, map[string]any{"hash": "abc", "name": "Night.of.the.Living.Dead.1968.1080p.BluRay.x264-GRP",
				"progress": progress, "state": "stalledUP", "content_path": f.content, "tags": tag, "size": 2 * gb})
		}
		json.NewEncoder(w).Encode(list)
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
  categorymappings: [{id: m, cat: Movies, desc: Movies}]
  modes: {search: [q], movie-search: [q]}
search:
  paths: [{path: api, response: {type: json}}]
  inputs: {q: "{{ .Keywords }}"}
  rows: {selector: r}
  fields:
    title: {selector: t}
    category: {text: m}
    download: {selector: l}
    seeders: {selector: s}
    size: {selector: z}
`

func TestAddSearchImport(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "media") // the drive, as this app sees it
	for _, sub := range []string{"downloads", "movies"} {
		os.MkdirAll(filepath.Join(media, sub), 0o755)
	}
	// The finished download, as qBittorrent would leave it.
	dl := filepath.Join(media, "downloads", "Night.of.the.Living.Dead.1968.1080p.BluRay.x264-GRP")
	os.MkdirAll(dl, 0o755)
	os.WriteFile(filepath.Join(dl, "movie.mkv"), []byte(strings.Repeat("x", 4096)), 0o644)
	os.WriteFile(filepath.Join(dl, "sample.mkv"), []byte("x"), 0o644)

	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"id":10331,"title":"Night of the Living Dead","original_title":"Night of the Living Dead",
			"release_date":"1968-10-04","imdb_id":"tt0063350","runtime":96,"poster_path":"/p.jpg"}`))
	}))
	defer tmdb.Close()
	metadata.TMDBBase = tmdb.URL

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"r":[
			{"t":"Night.of.the.Living.Dead.1968.1080p.BluRay.x264-GRP","l":"magnet:?xt=urn:btih:abc","s":"40","z":"2147483648"},
			{"t":"Night.of.the.Living.Dead.1968.2160p.Remux","l":"magnet:?xt=urn:btih:def","s":"90","z":"60 GB"}]}`))
	}))
	defer site.Close()

	qb := &fakeQBit{content: "/data/downloads/Night.of.the.Living.Dead.1968.1080p.BluRay.x264-GRP"}
	qsrv := httptest.NewServer(qb)
	defer qsrv.Close()

	db, _ := store.Open(filepath.Join(dir, "data.json"))
	_ = db.Update(func(d *store.Data) error {
		d.Settings.Metadata.TMDBKey = "k"
		d.Settings.QBittorrent.URL = qsrv.URL
		d.Settings.Folders = store.Folders{Movies: filepath.Join(media, "movies"), Downloads: filepath.Join(media, "downloads"),
			Shows: filepath.Join(media, "tv"), QBitRoot: "/data", AppRoot: media}
		return nil
	})
	defs := filepath.Join(dir, "defs")
	os.MkdirAll(defs, 0o755)
	os.WriteFile(filepath.Join(defs, "testsite.yml"), []byte(siteDef), 0o644)
	idx := indexers.New(db, defs)
	if _, err := idx.Add(indexers.Input{Kind: indexers.KindDefinition, Definition: "testsite", Enabled: true, URL: site.URL}); err != nil {
		t.Fatal(err)
	}
	s := New(db, idx)
	ctx := context.Background()

	m, err := s.Add(ctx, 10331, "1080p")
	if err != nil {
		t.Fatal(err)
	}
	if m.Year != 1968 || m.IMDBID != "tt0063350" || m.File != nil {
		t.Fatalf("added: %+v", m)
	}
	if _, err := s.Add(ctx, 10331, "1080p"); err != errExists {
		t.Errorf("second add: %v", err)
	}

	msg, err := s.SearchAndGrab(ctx, 10331)
	if err != nil || !strings.Contains(msg, "1080p.BluRay") {
		t.Fatalf("search: %q %v", msg, err)
	}
	if len(qb.added) != 1 || qb.added[0] != "oneforarr-movie-10331" {
		t.Fatalf("qBittorrent got %v", qb.added)
	}

	// Still downloading.
	if err := s.CheckDownloads(ctx); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(10331)
	if m.Download == nil || m.Download.Progress != 0.5 {
		t.Fatalf("progress: %+v", m.Download)
	}

	// Finished: imported as a hardlink, named for Jellyfin.
	qb.done = true
	if err := s.CheckDownloads(ctx); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Get(10331)
	want := filepath.Join(media, "movies", "Night of the Living Dead (1968)", "Night of the Living Dead (1968) Bluray-1080p.mkv")
	if m.Download != nil || m.File == nil || m.File.Path != want {
		t.Fatalf("after import: file %+v download %+v note %+v", m.File, m.Download, m.LastSearch)
	}
	a, _ := os.Stat(want)
	b, _ := os.Stat(filepath.Join(dl, "movie.mkv"))
	if !os.SameFile(a, b) {
		t.Error("not a hardlink")
	}

	// Remove with files deletes only the movie's folder.
	if err := s.Remove(10331, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(want)); !os.IsNotExist(err) {
		t.Error("movie folder still there")
	}
	if _, err := os.Stat(filepath.Join(dl, "movie.mkv")); err != nil {
		t.Error("the download (still seeding) was deleted")
	}
}

func TestExistingFileCountsAsDownloaded(t *testing.T) {
	dir := t.TempDir()
	folder := filepath.Join(dir, "movies", "Night of the Living Dead (1968)")
	os.MkdirAll(folder, 0o755)
	os.WriteFile(filepath.Join(folder, "Night.of.the.Living.Dead.1968.2160p.Remux.mkv"), []byte("xx"), 0o644)
	if f := findExisting(folder, time.Now()); f == nil || f.Quality != "Remux-2160p" {
		t.Errorf("existing: %+v", f)
	}
}

func TestUpgrades(t *testing.T) {
	up := store.MovieRules{Quality: "1080p", MaxSizeGB: 20, MinSeeders: 1, Upgrades: true}
	releases := []model.Release{
		rel("Night.of.the.Living.Dead.1968.720p.BluRay.x264", 1*gb, 50),
		rel("Night.of.the.Living.Dead.1968.1080p.WEB-DL", 2*gb, 50),
	}
	m := night
	m.File = &store.MediaFile{Path: "/m/Night of the Living Dead (1968) Bluray-720p.mkv", Quality: "Bluray-720p"}

	ds := Decide(m, up, releases)
	if !ds[0].Accepted || ds[0].Release.Title != "Night.of.the.Living.Dead.1968.1080p.WEB-DL" {
		t.Errorf("1080p should replace a 720p file: %+v", ds[0])
	}
	if ds[1].Accepted || !strings.Contains(strings.Join(ds[1].Reasons, ";"), "Not better") {
		t.Errorf("720p over 720p: %+v", ds[1].Reasons)
	}

	m.File = &store.MediaFile{Path: "/m/x.mkv", Quality: "WEBDL-1080p", Resolution: 1080}
	for _, d := range Decide(m, up, releases) {
		if d.Accepted {
			t.Errorf("accepted %s although 1080p (the preferred quality) is there", d.Release.Title)
		}
	}

	up.Upgrades = false
	m.File = &store.MediaFile{Path: "/m/x.mkv", Quality: "Bluray-720p"}
	for _, d := range Decide(m, up, releases) {
		if d.Accepted {
			t.Errorf("accepted %s with upgrades off", d.Release.Title)
		}
	}
}
