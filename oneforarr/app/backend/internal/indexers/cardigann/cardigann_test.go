package cardigann

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
)

// These definitions are written for the tests. They use the same features
// as real ones: a JSON API with several torrents per movie, and an HTML
// table whose download link is on a details page.

const jsonDef = `
id: testjson
name: Test JSON
type: public
encoding: UTF-8
links: [https://example.invalid/]
caps:
  categorymappings:
    - {id: 45, cat: Movies/HD, desc: "720p"}
    - {id: 46, cat: Movies/UHD, desc: "2160p"}
  modes:
    search: [q]
    movie-search: [q, imdbid]
search:
  paths:
    - path: "api/list.json"
      response:
        type: json
  inputs:
    query_term: "{{ if .Query.IMDBID }}{{ .Query.IMDBID }}{{ else }}{{ .Keywords }}{{ end }}"
    limit: 50
  keywordsfilters:
    - name: re_replace
      args: ["[^\\w]+", " "]
  rows:
    selector: data.movies
    attribute: torrents
    multiple: true
  fields:
    _quality:
      selector: quality
    category:
      selector: quality
      case:
        "720p": 45
        "2160p": 46
        "*": 45
    year:
      selector: ..year
    title:
      selector: ..title
      filters:
        - name: replace
          args: [":", ""]
        - name: append
          args: " ({{ .Result.year }}) {{ .Result._quality }}{{ if eq .Result._quality \"2160p\" }} UHD{{ end }}"
    download:
      selector: url
    infohash:
      selector: hash
    imdbid:
      selector: ..imdb_code
    date:
      selector: date_uploaded_unix
    size:
      selector: size_bytes
    seeders:
      selector: seeds
    leechers:
      selector: peers
`

const jsonAnswer = `{"data":{"movie_count":1,"movies":[{"title":"Night: of the Living Dead","year":1968,"imdb_code":"tt0063350",
 "torrents":[
  {"url":"https://example.invalid/t/A.torrent","hash":"AAAA","quality":"720p","seeds":12,"peers":3,"size_bytes":734003200,"date_uploaded_unix":1700000000},
  {"url":"https://example.invalid/t/B.torrent","hash":"BBBB","quality":"2160p","seeds":5,"peers":1,"size_bytes":6012954214,"date_uploaded_unix":1700000100}
 ]},{"title":"No torrents","year":2000}]}}`

func TestJSONSite(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/list.json" {
			http.NotFound(w, r)
			return
		}
		gotQuery = r.URL.Query().Get("query_term")
		w.Write([]byte(jsonAnswer))
	}))
	defer srv.Close()

	def, err := Parse([]byte(jsonDef))
	if err != nil {
		t.Fatal(err)
	}
	if ok, why := def.Supported(); !ok {
		t.Fatalf("not supported: %s", why)
	}
	site := &Site{Def: def, BaseURL: srv.URL + "/", HTTP: srv.Client()}

	got, err := site.Search(context.Background(), model.Query{Kind: "search", Text: "night: of the living-dead"})
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != "night of the living dead" {
		t.Errorf("keywords filter: sent %q", gotQuery)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(got), got)
	}
	a, b := got[0], got[1]
	if a.Title != "Night of the Living Dead (1968) 720p" || b.Title != "Night of the Living Dead (1968) 2160p UHD" {
		t.Errorf("titles: %q / %q", a.Title, b.Title)
	}
	if a.Size != 734003200 || a.Seeders != 12 || a.Leechers != 3 {
		t.Errorf("numbers: %+v", a)
	}
	if a.IMDBID != "tt0063350" || a.InfoHash != "aaaa" || !strings.HasPrefix(a.MagnetURL, "magnet:?xt=urn:btih:aaaa") {
		t.Errorf("ids: %+v", a)
	}
	if !a.PublishedAt.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("date: %v", a.PublishedAt)
	}
	if len(a.Categories) != 1 || a.Categories[0] != 2040 || b.Categories[0] != 2045 {
		t.Errorf("categories: %v / %v", a.Categories, b.Categories)
	}

	// A movie search by IMDb id uses it, since the definition supports it.
	_, _ = site.Search(context.Background(), model.Query{Kind: "movie", Text: "x", IMDBID: "tt0063350"})
	if gotQuery != "tt0063350" {
		t.Errorf("imdb search sent %q", gotQuery)
	}
}

const htmlDef = `
id: testhtml
name: Test HTML
type: public
links: [https://example.invalid/]
settings:
  - name: sort
    type: select
    label: Sort
    default: date
    options:
      date: created
      seeds: seeders
caps:
  categorymappings:
    - {id: "TV shows", cat: TV, desc: "TV shows"}
    - {id: Movies, cat: Movies, desc: Movies}
    - {id: Other, cat: Other, desc: Other}
  modes:
    search: [q]
    tv-search: [q, season, ep]
download:
  selectors:
    - selector: a.dl[href^="magnet:"]
      attribute: href
    - selector: a.dl
      attribute: href
search:
  paths:
    - path: "{{ if .Keywords }}search/all/{{ .Keywords }}/{{ .Config.sort }}/1/{{ else }}latest{{ end }}"
  rows:
    selector: table.results > tbody > tr[bgcolor]
  fields:
    title:
      selector: td.name > a
      attribute: href
      filters:
        - name: regexp
          args: "/(.+?)-torrent-\\d+\\.html"
        - name: re_replace
          args: ["-", " "]
    category:
      selector: td.added
      optional: true
      default: Other
      filters:
        - name: regexp
          args: " in (.+?)$"
    details:
      selector: td.name > a
      attribute: href
    download:
      selector: td.name > a
      attribute: href
    date:
      selector: td.added
      filters:
        - name: split
          args: ["-", 0]
        - name: replace
          args: ["+", " ago"]
    size:
      selector: td.size
    seeders:
      selector: .seeds
    leechers:
      selector: .leech
`

const htmlAnswer = `<html><body><table class="results"><tbody>
<tr><th>Name</th></tr>
<tr bgcolor="#fff"><td class="name"><a href="/Some-Show-S01E02-1080p-torrent-111.html">x</a></td>
  <td class="added">2 days+ - in TV shows</td><td class="size">1,2 GB</td><td class="seeds">1,024</td><td class="leech">7</td></tr>
<tr bgcolor="#eee"><td class="name"><a href="/Other-Thing-torrent-222.html">y</a></td>
  <td class="added">5 hours+</td><td class="size">700.5 MB</td><td class="seeds">3</td><td class="leech">0</td></tr>
</tbody></table></body></html>`

func TestHTMLSite(t *testing.T) {
	var gotPath string
	page := `<a class="dl" href="//cache.example.invalid/abc.torrent">t</a><a class="dl" href="magnet:?xt=urn:btih:abc">m</a>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/search/"):
			gotPath = r.URL.Path
			w.Write([]byte(htmlAnswer))
		case r.URL.Path == "/Some-Show-S01E02-1080p-torrent-111.html":
			w.Write([]byte(page))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	def, err := Parse([]byte(htmlDef))
	if err != nil {
		t.Fatal(err)
	}
	site := &Site{Def: def, BaseURL: srv.URL + "/", HTTP: srv.Client(), Config: map[string]any{"sort": "seeds"}}
	got, err := site.Search(context.Background(), model.Query{Kind: "tv", Text: "Some Show", Season: 1, Episode: 2})
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/search/all/Some Show S01E02/seeds/1/" {
		t.Errorf("path: %q", gotPath)
	}
	if len(got) != 2 {
		t.Fatalf("got %d results: %+v", len(got), got)
	}
	a, b := got[0], got[1]
	if a.Title != "Some Show S01E02 1080p" {
		t.Errorf("title %q", a.Title)
	}
	if a.Seeders != 1024 || a.Leechers != 7 {
		t.Errorf("peers: %+v", a)
	}
	if a.Size != parseSize("1.2 GB") || a.Size < 1288490000 || b.Size != 734527488 {
		t.Errorf("sizes %d %d", a.Size, b.Size)
	}
	if age := time.Since(a.PublishedAt); age < 47*time.Hour || age > 49*time.Hour {
		t.Errorf("date %v", a.PublishedAt)
	}
	if a.Categories[0] != 5000 || b.Categories[0] != 8000 {
		t.Errorf("categories %v %v", a.Categories, b.Categories)
	}
	if a.DownloadURL != srv.URL+"/Some-Show-S01E02-1080p-torrent-111.html" {
		t.Errorf("download %q", a.DownloadURL)
	}

	// The real link is on the details page. The magnet selector comes
	// first, but this magnet has no trackers, so the .torrent link wins.
	link, err := site.ResolveDownload(context.Background(), a.DownloadURL)
	if err != nil {
		t.Fatal(err)
	}
	if link != "http://cache.example.invalid/abc.torrent" {
		t.Errorf("resolved %q", link)
	}
	// A magnet with trackers is used as the definition says.
	page = `<a class="dl" href="//cache.example.invalid/abc.torrent">t</a><a class="dl" href="magnet:?xt=urn:btih:abc&tr=udp%3A%2F%2Ftracker.example%3A1337">m</a>`
	link, _ = site.ResolveDownload(context.Background(), a.DownloadURL)
	if !strings.HasPrefix(link, "magnet:?xt=urn:btih:abc&tr=") {
		t.Errorf("resolved %q", link)
	}
	// Only a bare magnet on the page: still better than nothing.
	page = `<a class="dl" href="magnet:?xt=urn:btih:abc">m</a>`
	link, _ = site.ResolveDownload(context.Background(), a.DownloadURL)
	if link != "magnet:?xt=urn:btih:abc" {
		t.Errorf("resolved %q", link)
	}
}

func TestUnsupported(t *testing.T) {
	cases := map[string]string{
		"private":  "id: a\ntype: private\nsearch: {path: x}\n",
		"login":    "id: a\ntype: public\nlogin: {path: login}\nsearch: {path: x}\n",
		"account":  "id: a\ntype: public\nsettings: [{name: username, type: text}]\nsearch: {path: x}\n",
		"encoding": "id: a\ntype: public\nencoding: windows-1251\nsearch: {path: x}\n",
		"xml":      "id: a\ntype: public\nsearch: {paths: [{path: x, response: {type: xml}}]}\n",
	}
	for name, src := range cases {
		d, err := Parse([]byte(src))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ok, _ := d.Supported(); ok {
			t.Errorf("%s: should not be supported", name)
		}
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"734003200":  734003200,
		"1.5 GB":     int64(1.5 * (1 << 30)),
		"700 MiB":    700 << 20,
		"1,234.5 MB": int64(1234.5 * (1 << 20)),
		"1.234,5 MB": int64(1234.5 * (1 << 20)),
		"12 KB":      12 << 10,
		"":           0,
	}
	for in, want := range cases {
		if got := parseSize(in); got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseDate(t *testing.T) {
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	cases := map[string]time.Time{
		"2021-10-25T16:44:43Z": time.Date(2021, 10, 25, 16, 44, 43, 0, time.UTC),
		"1700000000":           time.Unix(1700000000, 0).UTC(),
		"3 days ago":           now.AddDate(0, 0, -3),
		"1 hour 20 mins ago":   now.Add(-80 * time.Minute),
		"a month ago":          now.AddDate(0, -1, 0),
		"yesterday":            now.AddDate(0, 0, -1),
	}
	for in, want := range cases {
		if got := parseDate(in, now); !got.Equal(want) {
			t.Errorf("parseDate(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTemplates(t *testing.T) {
	data := templateData{
		"Config":     map[string]any{"titleOnly": true, "type": "_"},
		"Keywords":   "night",
		"Categories": []string{"movies", "tv"},
	}
	cases := map[string]string{
		`{{ if and .Config.titleOnly .Keywords }}title:({{ .Keywords }}){{ end }}`: "title:(night)",
		`mediatype:({{ join .Categories " OR " }})`:                                "mediatype:(movies OR tv)",
		`{{ re_replace .Config.type "_" "" }}x`:                                    "x",
		`{{ .Missing }}a`:                                                          "a",
	}
	for in, want := range cases {
		if got := render(in, data); got != want {
			t.Errorf("render(%q) = %q, want %q", in, got, want)
		}
	}
}
