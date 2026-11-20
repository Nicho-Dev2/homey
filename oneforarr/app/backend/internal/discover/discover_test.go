package discover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/metadata"
)

func TestPage(t *testing.T) {
	var calls atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch {
		case r.URL.Path == "/watch/providers/movie":
			if r.URL.Query().Get("watch_region") != "US" {
				t.Errorf("region: %s", r.URL.RawQuery)
			}
			w.Write([]byte(`{"results":[
				{"provider_id":2,"provider_name":"Streamy","logo_path":"/s.png","display_priorities":{"US":2}},
				{"provider_id":3,"provider_name":"Streamy with Ads","logo_path":"/a.png","display_priorities":{"US":1}},
				{"provider_id":1,"provider_name":"Flix","logo_path":"/f.png","display_priorities":{"US":0}}]}`))
		case strings.HasPrefix(r.URL.Path, "/trending/movie"), r.URL.Path == "/discover/movie":
			w.Write([]byte(`{"results":[
				{"id":10,"title":"Have It","release_date":"2020-01-01","poster_path":"/p"},
				{"id":11,"title":"Added Not Downloaded","release_date":"2021-01-01","poster_path":"/p"},
				{"id":12,"title":"New Movie","release_date":"2026-11-01","poster_path":"/p"},
				{"id":13,"title":"No Poster","release_date":"2026-11-01"}]}`))
		case strings.HasPrefix(r.URL.Path, "/trending/tv"), r.URL.Path == "/discover/tv":
			w.Write([]byte(`{"results":[
				{"id":20,"name":"Show I Have","first_air_date":"2019-01-01","poster_path":"/p"},
				{"id":21,"name":"New Show","first_air_date":"2026-10-01","poster_path":"/p"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	metadata.TMDBBase = api.URL

	db, _ := store.Open(filepath.Join(t.TempDir(), "data.json"))
	_ = db.Update(func(d *store.Data) error {
		d.Settings.Metadata.TMDBKey = "k"
		d.Movies = []store.Movie{{TMDBID: 10, File: &store.MediaFile{Path: "/x"}}, {TMDBID: 11}}
		d.Shows = []store.Show{{TMDBID: 20, Episodes: []store.Episode{{Season: 1, Number: 1, File: &store.MediaFile{Path: "/y"}}}}}
		return nil
	})
	cacheFile := filepath.Join(t.TempDir(), "discover-cache.json")
	s := New(db, cacheFile)
	page, err := s.Page(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, r := range page.Rows {
		titles = append(titles, r.Title)
	}
	want := "Trending movies this week|Trending shows this week|New on Flix|Popular on Flix|New on Streamy|Popular on Streamy"
	if strings.Join(titles, "|") != want {
		t.Errorf("rows: %v", titles)
	}
	newOnFlix := page.Rows[2].Items
	var got []string
	for _, it := range newOnFlix {
		label := it.Title.Title
		if it.InApp {
			label += "*"
		}
		got = append(got, label)
	}
	// Mixed movie, show, movie…; downloaded and poster-less titles left out.
	if strings.Join(got, "|") != "Added Not Downloaded*|New Show|New Movie" {
		t.Errorf("items: %v", got)
	}

	// The second page comes from the cache.
	before := calls.Load()
	if _, err := s.Page(context.Background(), false); err != nil || calls.Load() != before {
		t.Errorf("not cached: %d calls more", calls.Load()-before)
	}
	// After a restart, the saved cache is used too.
	again, err := New(db, cacheFile).Page(context.Background(), false)
	if err != nil || calls.Load() != before || len(again.Rows) != len(page.Rows) {
		t.Errorf("saved cache not used: %v, %d calls more", err, calls.Load()-before)
	}
	// Refresh fetches anew.
	if _, err := s.Page(context.Background(), true); err != nil || calls.Load() == before {
		t.Errorf("refresh didn't fetch: %v", err)
	}
}

func TestAmazonAndAppleLast(t *testing.T) {
	in := []metadata.Provider{{Name: "Netflix"}, {Name: "Amazon Prime Video"}, {Name: "Disney Plus"}, {Name: "Apple TV+"}, {Name: "Max"}}
	var got []string
	for _, p := range LastOnes(in) {
		got = append(got, p.Name)
	}
	if strings.Join(got, "|") != "Netflix|Disney Plus|Max|Amazon Prime Video|Apple TV+" {
		t.Errorf("order: %v", got)
	}
}
