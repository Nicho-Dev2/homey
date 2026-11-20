// Package indexers manages the sites and feeds the app searches, and
// searches them all at once.
package indexers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/indexers/cardigann"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/indexers/torznab"
)

// Kinds of indexer.
const (
	KindDefinition = "definition"
	KindTorznab    = "torznab"
)

// searchTimeout is how long one indexer gets to answer a search.
const searchTimeout = 40 * time.Second

// Service is the indexers part of the app.
type Service struct {
	Store   *store.Store
	Library *cardigann.Library
	HTTP    *http.Client

	mu    sync.Mutex
	paces map[string]*cardigann.Pace
}

// New returns the indexers service, with definition files in defsDir.
func New(s *store.Store, defsDir string) *Service {
	return &Service{
		Store:   s,
		Library: &cardigann.Library{Dir: defsDir},
		HTTP:    &http.Client{Timeout: searchTimeout},
	}
}

type searcher interface {
	Search(ctx context.Context, q model.Query) ([]model.Release, error)
}

// open readies one indexer for searching.
func (s *Service) open(ix store.Indexer) (searcher, error) {
	switch ix.Kind {
	case KindTorznab:
		return &torznab.Feed{URL: ix.URL, APIKey: ix.APIKey, HTTP: s.HTTP}, nil
	case KindDefinition:
		site, err := s.site(ix)
		if err != nil {
			return nil, err
		}
		return site, nil
	}
	return nil, fmt.Errorf("unknown indexer kind %q", ix.Kind)
}

func (s *Service) site(ix store.Indexer) (*cardigann.Site, error) {
	def, err := s.Library.Get(ix.Definition)
	if errors.Is(err, cardigann.ErrNoDefinition) {
		return nil, fmt.Errorf("The definition file %s.yml is missing from the definitions folder.", ix.Definition)
	}
	if err != nil {
		return nil, err
	}
	if ok, why := def.Supported(); !ok {
		return nil, errors.New(why)
	}
	return &cardigann.Site{Def: def, BaseURL: ix.URL, Config: ix.Settings, HTTP: s.HTTP, Pace: s.pace(ix.ID)}, nil
}

// pace returns the shared request pacing for one indexer.
func (s *Service) pace(id string) *cardigann.Pace {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paces == nil {
		s.paces = map[string]*cardigann.Pace{}
	}
	if s.paces[id] == nil {
		s.paces[id] = &cardigann.Pace{}
	}
	return s.paces[id]
}

// Failure is one indexer that couldn't answer a search.
type Failure struct {
	IndexerID   string `json:"indexerId"`
	IndexerName string `json:"indexerName"`
	Error       string `json:"error"`
}

// Results is what a search across all indexers found.
type Results struct {
	Releases []model.Release `json:"releases"`
	Failures []Failure       `json:"failures"`
}

// Search asks every enabled indexer at once and puts the answers together,
// most seeders first. One indexer failing doesn't stop the others.
func (s *Service) Search(ctx context.Context, q model.Query) Results {
	var enabled []store.Indexer
	for _, ix := range s.Store.Read().Indexers {
		if ix.Enabled {
			enabled = append(enabled, ix)
		}
	}
	type answer struct {
		ix       store.Indexer
		releases []model.Release
		err      error
	}
	answers := make(chan answer, len(enabled))
	for _, ix := range enabled {
		go func() {
			rels, err := s.searchOne(ctx, ix, q)
			answers <- answer{ix, rels, err}
		}()
	}
	out := Results{Releases: []model.Release{}, Failures: []Failure{}}
	for range enabled {
		a := <-answers
		if a.err != nil {
			out.Failures = append(out.Failures, Failure{a.ix.ID, a.ix.Name, a.err.Error()})
			continue
		}
		out.Releases = append(out.Releases, a.releases...)
	}
	sort.SliceStable(out.Releases, func(i, j int) bool { return out.Releases[i].Seeders > out.Releases[j].Seeders })
	sort.Slice(out.Failures, func(i, j int) bool { return out.Failures[i].IndexerName < out.Failures[j].IndexerName })
	return out
}

func (s *Service) searchOne(ctx context.Context, ix store.Indexer, q model.Query) ([]model.Release, error) {
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	src, err := s.open(ix)
	if err != nil {
		return nil, err
	}
	rels, err := src.Search(ctx, q)
	if err != nil {
		return nil, err
	}
	keep := rels[:0]
	for _, r := range rels {
		if !inCategories(r.Categories, q.Categories) {
			continue
		}
		r.IndexerID, r.IndexerName = ix.ID, ix.Name
		if r.Categories == nil {
			r.Categories = []int{}
		}
		keep = append(keep, r)
	}
	return keep, nil
}

// inCategories reports whether a result fits the wanted categories. A
// result with no category is kept: many sites don't say.
func inCategories(have, want []int) bool {
	if len(want) == 0 || len(have) == 0 {
		return true
	}
	for _, h := range have {
		for _, w := range want {
			if h == w || (w%1000 == 0 && model.CategoryGroup(h) == w) {
				return true
			}
		}
	}
	return false
}

// Test runs an empty search (the site's newest results) on an indexer,
// saved or not, and returns how many results came back.
func (s *Service) Test(ctx context.Context, ix store.Indexer) (int, error) {
	if ix.ID == "" {
		ix.ID = "test-" + ix.Definition
	}
	rels, err := s.searchOne(ctx, ix, model.Query{Kind: "search"})
	if err != nil {
		return 0, err
	}
	return len(rels), nil
}

// DownloadLink returns the link to give qBittorrent for a result: its
// magnet link, or its download address after any details-page lookup.
func (s *Service) DownloadLink(ctx context.Context, indexerID, downloadURL, magnetURL string) (string, error) {
	if strings.HasPrefix(magnetURL, "magnet:") && downloadURL == "" {
		return magnetURL, nil
	}
	ix, ok := s.Get(indexerID)
	if !ok {
		return "", errors.New("That indexer no longer exists.")
	}
	if downloadURL == "" {
		return "", errors.New("This result has no download link.")
	}
	if ix.Kind != KindDefinition {
		return downloadURL, nil
	}
	site, err := s.site(ix)
	if err != nil {
		return "", err
	}
	link, err := site.ResolveDownload(ctx, downloadURL)
	if err != nil && strings.HasPrefix(magnetURL, "magnet:") {
		return magnetURL, nil
	}
	return link, err
}

// Get returns one saved indexer.
func (s *Service) Get(id string) (store.Indexer, bool) {
	for _, ix := range s.Store.Read().Indexers {
		if ix.ID == id {
			return ix, true
		}
	}
	return store.Indexer{}, false
}

// ---- adding and changing ----

// Input is what the Add and Edit forms send.
type Input struct {
	Kind       string         `json:"kind"`
	Definition string         `json:"definition"`
	Name       string         `json:"name"`
	Enabled    bool           `json:"enabled"`
	URL        string         `json:"url"`
	APIKey     string         `json:"apiKey"` // empty keeps the saved one
	Settings   map[string]any `json:"settings"`
}

// build checks an Input and turns it into a stored indexer. saved is the
// indexer being changed (zero when adding).
func (s *Service) build(in Input, saved store.Indexer) (store.Indexer, error) {
	ix := store.Indexer{
		ID: saved.ID, Kind: in.Kind, Name: strings.TrimSpace(in.Name), Enabled: in.Enabled,
	}
	switch in.Kind {
	case KindDefinition:
		def, err := s.Library.Get(in.Definition)
		if err != nil {
			return ix, fmt.Errorf("There's no definition file named %s.yml.", in.Definition)
		}
		if ok, why := def.Supported(); !ok {
			return ix, errors.New(why)
		}
		ix.Definition = def.ID
		if ix.Name == "" {
			ix.Name = def.Name
		}
		u, err := siteAddress(in.URL, def.Links)
		if err != nil {
			return ix, err
		}
		ix.URL = u
		ix.Settings, err = cleanSettings(def, in.Settings)
		if err != nil {
			return ix, err
		}
	case KindTorznab:
		u, err := url.Parse(strings.TrimSpace(in.URL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return ix, errors.New("The feed address must start with http:// or https://.")
		}
		ix.URL = u.String()
		ix.APIKey = strings.TrimSpace(in.APIKey)
		if ix.APIKey == "" {
			ix.APIKey = saved.APIKey
		}
		if ix.Name == "" {
			return ix, errors.New("Give the feed a name.")
		}
	default:
		return ix, errors.New("Choose a site or a Torznab feed.")
	}
	return ix, nil
}

// siteAddress checks the chosen site address. Usually it's one of the
// definition's links; another address is allowed, since sites move.
func siteAddress(raw string, links []string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" && len(links) > 0 {
		raw = links[0]
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("The site address must start with http:// or https://.")
	}
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String(), nil
}

// cleanSettings keeps only the definition's own settings, with the right
// kind of value for each.
func cleanSettings(def *cardigann.Definition, in map[string]any) (map[string]any, error) {
	out := map[string]any{}
	for _, st := range def.Settings {
		v, given := in[st.Name]
		if strings.HasPrefix(st.Type, "info") {
			continue
		}
		if !given {
			out[st.Name] = st.DefaultValue()
			continue
		}
		switch st.Type {
		case "checkbox":
			b, ok := v.(bool)
			if !ok {
				return nil, fmt.Errorf("%s must be on or off.", labelOf(st))
			}
			out[st.Name] = b
		case "select":
			str, _ := v.(string)
			valid := slices.ContainsFunc(st.OptionList(), func(o cardigann.Option) bool { return o.Value == str })
			if !valid {
				return nil, fmt.Errorf("Choose one of the options for %s.", labelOf(st))
			}
			out[st.Name] = str
		default:
			str, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be text.", labelOf(st))
			}
			out[st.Name] = strings.TrimSpace(str)
		}
	}
	return out, nil
}

func labelOf(st cardigann.Setting) string {
	if st.Label != "" {
		return st.Label
	}
	return st.Name
}

// Add saves a new indexer.
func (s *Service) Add(in Input) (store.Indexer, error) {
	ix, err := s.build(in, store.Indexer{})
	if err != nil {
		return ix, err
	}
	ix.ID = newID()
	err = s.Store.Update(func(d *store.Data) error {
		d.Indexers = append(d.Indexers, ix)
		return nil
	})
	return ix, err
}

// Change saves new values for an indexer.
func (s *Service) Change(id string, in Input) (store.Indexer, error) {
	saved, ok := s.Get(id)
	if !ok {
		return store.Indexer{}, errNotFound
	}
	ix, err := s.build(in, saved)
	if err != nil {
		return ix, err
	}
	err = s.Store.Update(func(d *store.Data) error {
		for i := range d.Indexers {
			if d.Indexers[i].ID == id {
				d.Indexers[i] = ix
				return nil
			}
		}
		return errNotFound
	})
	return ix, err
}

// Remove deletes an indexer.
func (s *Service) Remove(id string) error {
	return s.Store.Update(func(d *store.Data) error {
		for i := range d.Indexers {
			if d.Indexers[i].ID == id {
				d.Indexers = slices.Delete(d.Indexers, i, i+1)
				return nil
			}
		}
		return errNotFound
	})
}

var errNotFound = errors.New("That indexer doesn't exist (any more).")

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
