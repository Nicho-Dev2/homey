package torznab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
)

const answer = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed">
<channel><title>Test</title>
<item>
  <title>Night of the Living Dead 1968 1080p</title>
  <guid>https://site.invalid/details/1</guid>
  <comments>https://site.invalid/details/1</comments>
  <pubDate>Mon, 02 Jan 2006 15:04:05 +0000</pubDate>
  <size>1610612736</size>
  <enclosure url="https://site.invalid/dl/1.torrent" length="1610612736" type="application/x-bittorrent"/>
  <torznab:attr name="category" value="2040"/>
  <torznab:attr name="seeders" value="40"/>
  <torznab:attr name="peers" value="44"/>
  <torznab:attr name="infohash" value="ABC123"/>
  <torznab:attr name="imdb" value="63350"/>
</item>
</channel></rss>`

func TestSearch(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Path + "?" + r.URL.RawQuery
		if r.URL.Query().Get("apikey") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`<error code="100" description="Invalid API Key"/>`))
			return
		}
		w.Write([]byte(answer))
	}))
	defer srv.Close()

	f := &Feed{URL: srv.URL + "/1/", APIKey: "k", HTTP: srv.Client()}
	res, err := f.Search(context.Background(), model.Query{Kind: "movie", Text: "night", IMDBID: "tt0063350", Categories: []int{2000}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "/1/api?") || !strings.Contains(got, "t=movie") || !strings.Contains(got, "imdbid=tt0063350") || !strings.Contains(got, "cat=2000") {
		t.Errorf("request %q", got)
	}
	if len(res) != 1 {
		t.Fatalf("got %d", len(res))
	}
	r := res[0]
	if r.Seeders != 40 || r.Leechers != 4 || r.Size != 1610612736 || r.InfoHash != "abc123" || r.IMDBID != "tt0063350" ||
		r.DownloadURL != "https://site.invalid/dl/1.torrent" || r.Categories[0] != 2040 || r.PublishedAt.Year() != 2006 {
		t.Errorf("parsed %+v", r)
	}

	f.APIKey = "wrong"
	if _, err := f.Search(context.Background(), model.Query{}); err == nil || !strings.Contains(err.Error(), "Invalid API Key") {
		t.Errorf("error %v", err)
	}
}
