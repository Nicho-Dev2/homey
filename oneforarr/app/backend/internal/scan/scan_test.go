package scan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/indexers"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/metadata"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/movies"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/shows"
)

func TestReadName(t *testing.T) {
	cases := []struct {
		name     string
		file     bool
		title    string
		year, id int
	}{
		{"Alien (1979)", false, "Alien", 1979, 0},
		{"Alien (1979) {tmdb-348}", false, "Alien", 1979, 348},
		{"Alien (1979) [tmdbid-348]", false, "Alien", 1979, 348},
		{"Alien.1979.1080p.BluRay.x264-GRP.mkv", true, "Alien", 1979, 0},
		{"Severance", false, "Severance", 0, 0},
	}
	for _, c := range cases {
		title, year, id := readName(c.name, c.file)
		if title != c.title || year != c.year || id != c.id {
			t.Errorf("%q: got %q %d %d", c.name, title, year, id)
		}
	}
}

func TestPick(t *testing.T) {
	cands := []candidate{{1, "Halloween", "", 1978}, {2, "Halloween", "", 2018}, {3, "Halloween II", "", 1981}}
	if id, _ := pick("Halloween", 2018, cands); id != 2 {
		t.Errorf("with year: %d", id)
	}
	if id, note := pick("Halloween", 0, cands); id != 0 || !strings.Contains(note, "2 with that exact title") {
		t.Errorf("no year: %d %q", id, note)
	}
	if id, _ := pick("Halloween", 2007, cands); id != 0 {
		t.Errorf("wrong year matched: %d", id)
	}
	if id, _ := pick("Le Fabuleux Destin", 2001, []candidate{{9, "Amélie", "Le Fabuleux Destin", 2001}}); id != 9 {
		t.Errorf("original title: %d", id)
	}
}

// A full scan: a sure movie folder and a loose file are added, an unsure
// one is listed, a known movie's file that's gone is forgotten, a file
// dropped in a known show's folder is picked up, and a new show is added
// following nothing.
func TestScan(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		switch {
		case r.URL.Path == "/search/movie" && q == "Alien":
			w.Write([]byte(`{"results":[{"id":348,"title":"Alien","release_date":"1979-05-25"},{"id":8077,"title":"Alien 3","release_date":"1992-05-22"}]}`))
		case r.URL.Path == "/search/movie" && q == "Heat":
			w.Write([]byte(`{"results":[{"id":949,"title":"Heat","release_date":"1995-12-15"}]}`))
		case r.URL.Path == "/search/movie":
			w.Write([]byte(`{"results":[{"id":5,"title":"Something Else","release_date":"2001-01-01"}]}`))
		case r.URL.Path == "/movie/348":
			w.Write([]byte(`{"id":348,"title":"Alien","release_date":"1979-05-25"}`))
		case r.URL.Path == "/movie/949":
			w.Write([]byte(`{"id":949,"title":"Heat","release_date":"1995-12-15"}`))
		case r.URL.Path == "/movie/10":
			w.Write([]byte(`{"id":10,"title":"Gone Movie","release_date":"2000-01-01"}`))
		case r.URL.Path == "/search/tv":
			w.Write([]byte(`{"results":[{"id":77,"name":"Tiny Show","first_air_date":"2020-01-01"}]}`))
		case r.URL.Path == "/tv/77":
			w.Write([]byte(`{"id":77,"name":"Tiny Show","first_air_date":"2020-01-01","external_ids":{"tvdb_id":555}}`))
		case r.URL.Path == "/lookup/shows":
			w.Write([]byte(`{"id":9}`))
		case r.URL.Path == "/shows/9/episodes":
			w.Write([]byte(`[{"season":1,"number":1,"name":"One","airdate":"2020-01-01"},{"season":1,"number":2,"name":"Two","airdate":"2020-01-08"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	metadata.TMDBBase, metadata.TVmazeBase = api.URL, api.URL

	dir := t.TempDir()
	mv, tv := filepath.Join(dir, "movies"), filepath.Join(dir, "tv")
	write := func(p string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("video"), 0o644)
	}
	write(filepath.Join(mv, "Alien (1979)", "Alien (1979) Bluray-1080p.mkv"))
	write(filepath.Join(mv, "Heat.1995.720p.BluRay.x264-GRP.mkv"))
	write(filepath.Join(mv, "Mystery Film (2003)", "Mystery Film (2003).mkv"))
	write(filepath.Join(mv, "Skipped (2010)", "Skipped.mkv"))
	write(filepath.Join(tv, "Tiny Show", "Season 01", "Tiny Show - S01E01.mkv"))

	db, _ := store.Open(filepath.Join(dir, "data.json"))
	_ = db.Update(func(d *store.Data) error {
		d.Settings.Metadata.TMDBKey = "k"
		d.Settings.Folders.Movies, d.Settings.Folders.Shows = mv, tv
		d.Movies = []store.Movie{{TMDBID: 10, Title: "Gone Movie", Year: 2000, Quality: "1080p",
			File: &store.MediaFile{Path: filepath.Join(mv, "Gone Movie (2000)", "x.mkv")}}}
		d.Library.Skipped = []string{filepath.Join(mv, "Skipped (2010)")}
		return nil
	})
	idx := indexers.New(db, filepath.Join(dir, "defs"))
	mov, shw := movies.New(db, idx), shows.New(db, idx)
	sc := New(db, mov, shw)

	report := sc.Scan(context.Background())
	t.Log(report)

	d := db.Read()
	byID := map[int]store.Movie{}
	for _, m := range d.Movies {
		byID[m.TMDBID] = m
	}
	if m := byID[348]; m.File == nil || m.Folder != "" {
		t.Errorf("Alien: %+v", m)
	}
	if m := byID[949]; m.File == nil || !strings.HasSuffix(m.File.Path, ".mkv") || m.File.Resolution != 720 {
		t.Errorf("Heat (loose file): %+v", m.File)
	}
	if byID[10].File != nil {
		t.Error("gone file still saved")
	}
	if len(d.Library.Unmatched) != 1 || d.Library.Unmatched[0].Title != "Mystery Film" {
		t.Fatalf("unmatched: %+v", d.Library.Unmatched)
	}
	if len(d.Shows) != 1 || d.Shows[0].Folder != "Tiny Show" || d.Shows[0].Monitor != shows.MonitorNone {
		t.Fatalf("shows: %+v", d.Shows)
	}
	if e := d.Shows[0].Episodes; e[0].File == nil || e[1].File != nil || e[0].Monitored || e[1].Monitored {
		t.Errorf("episodes: %+v", e)
	}

	// A second scan adds nothing new and finds the episode dropped in later.
	write(filepath.Join(tv, "Tiny Show", "Season 01", "Tiny.Show.S01E02.1080p.WEB.mkv"))
	report = sc.Scan(context.Background())
	if !strings.Contains(report, "Found 1 new file") || strings.Contains(report, "Added") {
		t.Errorf("second scan: %s", report)
	}

	// Picking the right movie for the unmatched folder.
	if _, err := sc.Match(context.Background(), filepath.Join(mv, "Mystery Film (2003)"), 949); err == nil {
		t.Error("matched to a movie already in the app")
	}
	if err := sc.Skip(filepath.Join(mv, "Mystery Film (2003)")); err != nil {
		t.Fatal(err)
	}
	if d := db.Read(); len(d.Library.Unmatched) != 0 || len(d.Library.Skipped) != 2 {
		t.Errorf("after skip: %+v", d.Library)
	}
}

// An unplugged drive must not make files look gone.
func TestScanEmptyFolderForgetsNothing(t *testing.T) {
	dir := t.TempDir()
	mv := filepath.Join(dir, "movies")
	os.MkdirAll(mv, 0o755)
	db, _ := store.Open(filepath.Join(dir, "data.json"))
	_ = db.Update(func(d *store.Data) error {
		d.Settings.Folders.Movies = mv
		d.Settings.Folders.Shows = filepath.Join(dir, "missing")
		d.Movies = []store.Movie{{TMDBID: 1, Title: "A", File: &store.MediaFile{Path: filepath.Join(mv, "A", "a.mkv")}}}
		return nil
	})
	idx := indexers.New(db, dir)
	report := New(db, movies.New(db, idx), shows.New(db, idx)).Scan(context.Background())
	if db.Read().Movies[0].File == nil || !strings.Contains(report, "drive connected") {
		t.Errorf("report %q, file %v", report, db.Read().Movies[0].File)
	}
}
