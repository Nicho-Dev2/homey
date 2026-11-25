// Package discover builds the Discover page: rows of movies and shows that
// are trending, new or popular on the big streaming services, like browsing
// a streaming app. Everything comes from TMDB (its streaming data comes
// from JustWatch). Titles already downloaded are left out.
package discover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/metadata"
)

const (
	keepFor   = 6 * time.Hour // TMDB lists are fetched again after this
	services  = 8             // how many streaming services get rows
	rowLength = 20
)

// Row is one shelf on the page.
type Row struct {
	ID       string           `json:"id"`
	Title    string           `json:"title"`
	LogoPath string           `json:"logoPath,omitempty"` // the streaming service's logo
	Items    []metadata.Title `json:"items"`
}

// Item is a title as the page gets it: InApp marks one already added (but
// not downloaded yet).
type Item struct {
	metadata.Title
	InApp bool `json:"inApp"`
}

// PageRow is a Row with the library filter applied.
type PageRow struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	LogoPath string `json:"logoPath,omitempty"`
	Items    []Item `json:"items"`
}

// Page is what GET /api/discover answers.
type Page struct {
	Region    string    `json:"region"`
	UpdatedAt time.Time `json:"updatedAt"`
	Rows      []PageRow `json:"rows"`
	Problems  []string  `json:"problems,omitempty"` // rows TMDB couldn't give
}

// Service builds and caches the rows. The cache is also saved to a file,
// so a restart or an update of the app doesn't fetch the lists again.
type Service struct {
	Store     *store.Store
	Now       func() time.Time
	CacheFile string // empty: memory only

	mu    sync.Mutex
	cache map[string]cached // by region
}

// cacheVersion changes when the saved lists gain something new (v2: release
// dates), so lists saved before are fetched again.
const cacheVersion = 2

type cached struct {
	Version  int       `json:"version"`
	At       time.Time `json:"at"`
	Rows     []Row     `json:"rows"`
	Problems []string  `json:"problems,omitempty"`
}

// New returns the service, with the cache saved in cacheFile (if set).
func New(s *store.Store, cacheFile string) *Service {
	sv := &Service{Store: s, Now: time.Now, CacheFile: cacheFile, cache: map[string]cached{}}
	if cacheFile != "" {
		if raw, err := os.ReadFile(cacheFile); err == nil {
			_ = json.Unmarshal(raw, &sv.cache)
		}
	}
	return sv
}

// save writes the cache to its file. Losing it only means fetching again.
func (s *Service) save() {
	if s.CacheFile == "" {
		return
	}
	s.mu.Lock()
	raw, err := json.Marshal(s.cache)
	s.mu.Unlock()
	if err == nil {
		tmp := s.CacheFile + ".tmp"
		if os.WriteFile(tmp, raw, 0o600) == nil {
			_ = os.Rename(tmp, s.CacheFile)
		}
	}
}

// Page returns the rows for the saved country, from the cache when it's
// fresh (refresh forces new lists), without titles already downloaded.
func (s *Service) Page(ctx context.Context, refresh bool) (Page, error) {
	d := s.Store.Read()
	key, region := d.Settings.Metadata.TMDBKey, d.Settings.Metadata.Region
	if key == "" {
		return Page{}, errors.New("Add a TMDB key in Settings to use Discover.")
	}
	if region == "" {
		region = "US"
	}
	s.mu.Lock()
	c, ok := s.cache[region]
	s.mu.Unlock()
	if !ok || refresh || c.Version != cacheVersion || s.Now().Sub(c.At) > keepFor {
		rows, problems, err := s.build(ctx, metadata.NewTMDB(key), region)
		if err != nil {
			return Page{}, err
		}
		c = cached{Version: cacheVersion, At: s.Now(), Rows: rows, Problems: problems}
		s.mu.Lock()
		s.cache[region] = c
		s.mu.Unlock()
		s.save()
	}
	return Page{Region: region, UpdatedAt: c.At, Rows: filter(c.Rows, d, s.Now()), Problems: c.Problems}, nil
}

// filter drops titles already downloaded (a movie with its file, a show
// with any episode file) and movies not released yet, and marks the ones
// added but not downloaded.
func filter(rows []Row, d store.Data, now time.Time) []PageRow {
	today := now.Format("2006-01-02")
	have, added := map[string]bool{}, map[string]bool{}
	for _, m := range d.Movies {
		k := "movie:" + strconv.Itoa(m.TMDBID)
		added[k] = true
		if m.File != nil {
			have[k] = true
		}
	}
	for _, sh := range d.Shows {
		k := "show:" + strconv.Itoa(sh.TMDBID)
		added[k] = true
		for _, e := range sh.Episodes {
			if e.File != nil {
				have[k] = true
				break
			}
		}
	}
	out := make([]PageRow, 0, len(rows))
	for _, r := range rows {
		pr := PageRow{ID: r.ID, Title: r.Title, LogoPath: r.LogoPath, Items: []Item{}}
		for _, it := range r.Items {
			k := it.Kind + ":" + strconv.Itoa(it.TMDBID)
			unreleased := it.Kind == "movie" && it.ReleaseDate > today // dates compare as text
			if !have[k] && !unreleased {
				pr.Items = append(pr.Items, Item{Title: it, InApp: added[k]})
			}
		}
		if len(pr.Items) > 0 {
			out = append(out, pr)
		}
	}
	return out
}

// list is one TMDB request behind a row.
type list struct {
	kind, path string
	query      url.Values
}

// plan is a row before it's fetched: its lists are mixed together.
type plan struct {
	Row
	lists []list
}

// build fetches every row. A row that fails is reported, not fatal.
func (s *Service) build(ctx context.Context, tmdb *metadata.TMDB, region string) ([]Row, []string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	providers, err := tmdb.Providers(ctx, region)
	if err != nil {
		return nil, nil, err
	}
	if len(providers) > services {
		providers = providers[:services]
	}
	plans := Plans(region, LastOnes(providers), s.Now())

	rows := make([]Row, len(plans))
	problems := make([]string, len(plans))
	var wg sync.WaitGroup
	limit := make(chan struct{}, 6) // TMDB allows plenty; stay polite
	for i, p := range plans {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var lists [][]metadata.Title
			for _, l := range p.lists {
				limit <- struct{}{}
				items, err := tmdb.List(ctx, l.kind, l.path, cloneValues(l.query))
				<-limit
				if err != nil {
					problems[i] = fmt.Sprintf("%s: %v", p.Title, err)
					continue
				}
				lists = append(lists, items)
			}
			p.Items = mix(lists, rowLength)
			rows[i] = p.Row
		}()
	}
	wg.Wait()

	var outRows []Row
	var outProblems []string
	for i := range rows {
		if len(rows[i].Items) > 0 {
			outRows = append(outRows, rows[i])
		}
		if problems[i] != "" {
			outProblems = append(outProblems, problems[i])
		}
	}
	if len(outRows) == 0 && len(outProblems) > 0 {
		return nil, nil, errors.New("TMDB couldn't give any lists: " + outProblems[0])
	}
	return outRows, outProblems, nil
}

// Plans lists the rows: trending first, then "New on" and "Popular on"
// for each streaming service.
func Plans(region string, providers []metadata.Provider, now time.Time) []plan {
	today := now.Format("2006-01-02")
	plans := []plan{
		{Row: Row{ID: "trending-movies", Title: "Trending movies this week"},
			lists: []list{{"movie", "/trending/movie/week", nil}}},
		{Row: Row{ID: "trending-shows", Title: "Trending shows this week"},
			lists: []list{{"show", "/trending/tv/week", nil}}},
	}
	for _, p := range providers {
		on := func(extra url.Values) url.Values {
			q := url.Values{
				"watch_region":                  {region},
				"with_watch_providers":          {strconv.Itoa(p.ID)},
				"with_watch_monetization_types": {"flatrate"},
			}
			for k, v := range extra {
				q[k] = v
			}
			return q
		}
		id := strconv.Itoa(p.ID)
		plans = append(plans,
			plan{Row: Row{ID: "new-" + id, Title: "New on " + p.Name, LogoPath: p.LogoPath}, lists: []list{
				{"movie", "/discover/movie", on(url.Values{"sort_by": {"primary_release_date.desc"},
					"primary_release_date.lte": {today}, "vote_count.gte": {"20"}})},
				{"show", "/discover/tv", on(url.Values{"sort_by": {"first_air_date.desc"},
					"first_air_date.lte": {today}, "vote_count.gte": {"10"}})},
			}},
			plan{Row: Row{ID: "popular-" + id, Title: "Popular on " + p.Name, LogoPath: p.LogoPath}, lists: []list{
				{"movie", "/discover/movie", on(url.Values{"sort_by": {"popularity.desc"}})},
				{"show", "/discover/tv", on(url.Values{"sort_by": {"popularity.desc"}})},
			}},
		)
	}
	return plans
}

// putLast names the services whose rows go after all the others (the
// owner's choice): Amazon's and Apple's.
var putLast = []string{"amazon", "apple"}

// LastOnes keeps the services in order but moves those named in putLast
// to the end.
func LastOnes(providers []metadata.Provider) []metadata.Provider {
	var first, last []metadata.Provider
	for _, p := range providers {
		name := strings.ToLower(p.Name)
		if slices.ContainsFunc(putLast, func(w string) bool { return strings.Contains(name, w) }) {
			last = append(last, p)
		} else {
			first = append(first, p)
		}
	}
	return append(first, last...)
}

// mix takes turns between the lists (movie, show, movie, …) up to n titles,
// skipping repeats.
func mix(lists [][]metadata.Title, n int) []metadata.Title {
	var out []metadata.Title
	seen := map[string]bool{}
	for i := 0; len(out) < n; i++ {
		added := false
		for _, l := range lists {
			if i < len(l) {
				added = true
				k := l[i].Kind + ":" + strconv.Itoa(l[i].TMDBID)
				if !seen[k] && len(out) < n {
					seen[k] = true
					out = append(out, l[i])
				}
			}
		}
		if !added {
			break
		}
	}
	return out
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}
