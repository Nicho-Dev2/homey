package indexers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
)

// A small JSON site definition, written for the test.
const siteDef = `
id: mysite
name: My Site
type: public
links: [https://mysite.invalid/]
settings:
  - name: sort
    type: select
    label: Sort by
    default: date
    options: {date: Date, seeds: Seeders}
  - name: titleOnly
    type: checkbox
    label: Title only
    default: true
  - name: info_x
    type: info
    label: About
    default: Just text.
caps:
  categorymappings:
    - {id: m, cat: Movies, desc: Movies}
    - {id: t, cat: TV, desc: TV}
  modes: {search: [q]}
search:
  paths:
    - {path: "api", response: {type: json}}
  inputs: {q: "{{ .Keywords }}", sort: "{{ .Config.sort }}"}
  rows: {selector: results}
  fields:
    title: {selector: name}
    category: {selector: cat}
    download: {selector: link}
    seeders: {selector: s}
`

func setup(t *testing.T, answer string) (*Service, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "broken" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(answer))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	defs := filepath.Join(dir, "definitions")
	os.MkdirAll(defs, 0o755)
	os.WriteFile(filepath.Join(defs, "mysite.yml"), []byte(siteDef), 0o644)
	db, err := store.Open(filepath.Join(dir, "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, defs)
	s.HTTP = srv.Client()
	return s, srv
}

const twoResults = `{"results":[
 {"name":"A Movie 2020 1080p","cat":"m","link":"/a.torrent","s":"5"},
 {"name":"A Show S01E01","cat":"t","link":"/b.torrent","s":"50"}]}`

func TestAddSearchAndFilter(t *testing.T) {
	s, srv := setup(t, twoResults)
	ix, err := s.Add(Input{Kind: KindDefinition, Definition: "mysite", Enabled: true, URL: srv.URL,
		Settings: map[string]any{"sort": "seeds", "info_x": "ignored", "unknown": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if ix.Name != "My Site" || ix.URL != srv.URL+"/" {
		t.Errorf("defaults: %+v", ix)
	}
	if ix.Settings["sort"] != "seeds" || ix.Settings["titleOnly"] != true || len(ix.Settings) != 2 {
		t.Errorf("settings: %v", ix.Settings)
	}

	res := s.Search(context.Background(), model.Query{Text: "a"})
	if len(res.Failures) != 0 || len(res.Releases) != 2 {
		t.Fatalf("search: %+v", res)
	}
	if res.Releases[0].Seeders != 50 || res.Releases[0].IndexerName != "My Site" {
		t.Errorf("order/labels: %+v", res.Releases[0])
	}
	if res.Releases[0].DownloadURL != srv.URL+"/b.torrent" {
		t.Errorf("link: %q", res.Releases[0].DownloadURL)
	}

	// Only movies.
	res = s.Search(context.Background(), model.Query{Text: "a", Categories: []int{model.CategoryMovies}})
	if len(res.Releases) != 1 || res.Releases[0].Categories[0] != 2000 {
		t.Errorf("movie filter: %+v", res.Releases)
	}
}

func TestOneFailureDoesNotStopOthers(t *testing.T) {
	s, srv := setup(t, twoResults)
	_, _ = s.Add(Input{Kind: KindDefinition, Definition: "mysite", Name: "Good", Enabled: true, URL: srv.URL})
	_, _ = s.Add(Input{Kind: KindDefinition, Definition: "mysite", Name: "Off", Enabled: false, URL: srv.URL})
	_, _ = s.Add(Input{Kind: KindTorznab, Name: "Dead feed", Enabled: true, URL: "http://127.0.0.1:1/"})
	res := s.Search(context.Background(), model.Query{Text: "x"})
	if len(res.Releases) != 2 {
		t.Errorf("results: %d", len(res.Releases))
	}
	if len(res.Failures) != 1 || res.Failures[0].IndexerName != "Dead feed" {
		t.Errorf("failures: %+v", res.Failures)
	}
}

func TestBadInput(t *testing.T) {
	s, srv := setup(t, twoResults)
	bad := []Input{
		{Kind: KindDefinition, Definition: "nope", URL: srv.URL},
		{Kind: KindDefinition, Definition: "../../etc/passwd"},
		{Kind: KindDefinition, Definition: "mysite", URL: "ftp://x"},
		{Kind: KindDefinition, Definition: "mysite", Settings: map[string]any{"sort": "other"}},
		{Kind: KindDefinition, Definition: "mysite", Settings: map[string]any{"titleOnly": "yes"}},
		{Kind: KindTorznab, Name: "x", URL: "not a url"},
		{Kind: KindTorznab, URL: "http://x/"},
		{Kind: "other"},
	}
	for _, in := range bad {
		if _, err := s.Add(in); err == nil {
			t.Errorf("accepted %+v", in)
		}
	}
}

func TestAPIKeyKeptAndHidden(t *testing.T) {
	s, _ := setup(t, twoResults)
	ix, err := s.Add(Input{Kind: KindTorznab, Name: "Feed", URL: "http://feed.invalid/1/", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ix, err = s.Change(ix.ID, Input{Kind: KindTorznab, Name: "Feed 2", URL: "http://feed.invalid/1/"})
	if err != nil {
		t.Fatal(err)
	}
	if ix.APIKey != "secret" || ix.Name != "Feed 2" {
		t.Errorf("changed: %+v", ix)
	}
	v := viewOf(ix)
	if !v.APIKeySet {
		t.Error("apiKeySet false")
	}
	if err := s.Remove(ix.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ix.ID); err == nil {
		t.Error("removed twice")
	}
}

func TestDownloadLinkStaysOnSite(t *testing.T) {
	s, srv := setup(t, twoResults)
	// A definition with a details-page lookup.
	def := strings.Replace(siteDef, "search:", "download:\n  selectors:\n    - {selector: a.dl, attribute: href}\nsearch:", 1)
	os.WriteFile(filepath.Join(s.Library.Dir, "mysite.yml"), []byte(def), 0o644)
	ix, _ := s.Add(Input{Kind: KindDefinition, Definition: "mysite", Enabled: true, URL: srv.URL})
	if _, err := s.DownloadLink(context.Background(), ix.ID, "http://elsewhere.invalid/page", ""); err == nil {
		t.Error("followed a link off the site")
	}
}
