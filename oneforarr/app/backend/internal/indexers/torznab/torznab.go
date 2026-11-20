// Package torznab searches a Torznab or Newznab feed: the standard API that
// Jackett, Prowlarr and many sites offer. One address and an API key.
package torznab

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
)

// Feed is one Torznab address, e.g. http://host:9696/1/api.
type Feed struct {
	URL    string
	APIKey string
	HTTP   *http.Client
}

// Search asks the feed and returns its results.
func (f *Feed) Search(ctx context.Context, q model.Query) ([]model.Release, error) {
	params := url.Values{}
	switch {
	case q.Kind == "movie":
		params.Set("t", "movie")
		if q.IMDBID != "" {
			params.Set("imdbid", q.IMDBID)
		}
	case q.Kind == "tv":
		params.Set("t", "tvsearch")
		if q.Season > 0 {
			params.Set("season", strconv.Itoa(q.Season))
		}
		if q.Episode > 0 {
			params.Set("ep", strconv.Itoa(q.Episode))
		}
	default:
		params.Set("t", "search")
	}
	if q.Text != "" {
		params.Set("q", q.Text)
	}
	if len(q.Categories) > 0 {
		cats := make([]string, len(q.Categories))
		for i, c := range q.Categories {
			cats[i] = strconv.Itoa(c)
		}
		params.Set("cat", strings.Join(cats, ","))
	}
	if f.APIKey != "" {
		params.Set("apikey", f.APIKey)
	}
	page, err := f.get(ctx, params)
	if err != nil {
		return nil, err
	}
	return parse(page)
}

func (f *Feed) get(ctx context.Context, params url.Values) ([]byte, error) {
	u, err := url.Parse(strings.TrimSpace(f.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("The feed address must start with http:// or https://.")
	}
	if !strings.HasSuffix(u.Path, "/api") {
		u.Path = strings.TrimRight(u.Path, "/") + "/api"
	}
	q := u.Query()
	for k, vs := range params {
		q[k] = vs
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Couldn't reach %s.", u.Host)
	}
	defer resp.Body.Close()
	page, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("Reading the answer from %s failed.", u.Host)
	}
	if resp.StatusCode != http.StatusOK {
		if msg := feedError(page); msg != "" {
			return nil, errors.New("The feed said: " + msg)
		}
		return nil, fmt.Errorf("The feed answered %d %s.", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return page, nil
}

type rss struct {
	Items []item `xml:"channel>item"`
}

type item struct {
	Title     string `xml:"title"`
	GUID      string `xml:"guid"`
	Link      string `xml:"link"`
	Comments  string `xml:"comments"`
	PubDate   string `xml:"pubDate"`
	Size      int64  `xml:"size"`
	Enclosure struct {
		URL    string `xml:"url,attr"`
		Length int64  `xml:"length,attr"`
	} `xml:"enclosure"`
	// torznab:attr and newznab:attr: extra values by name.
	Attrs []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"attr"`
}

// feedError reads <error code="100" description="..."/>.
func feedError(page []byte) string {
	var e struct {
		XMLName     xml.Name `xml:"error"`
		Description string   `xml:"description,attr"`
	}
	if xml.Unmarshal(page, &e) == nil {
		return e.Description
	}
	return ""
}

func parse(page []byte) ([]model.Release, error) {
	if msg := feedError(page); msg != "" {
		return nil, errors.New("The feed said: " + msg)
	}
	var doc rss
	if err := xml.Unmarshal(page, &doc); err != nil {
		return nil, errors.New("The feed's answer wasn't valid Torznab.")
	}
	out := make([]model.Release, 0, len(doc.Items))
	for _, it := range doc.Items {
		r := model.Release{
			Title:      strings.TrimSpace(it.Title),
			Size:       it.Size,
			DetailsURL: firstNonEmpty(it.Comments, it.GUID),
		}
		if r.Size == 0 {
			r.Size = it.Enclosure.Length
		}
		link := firstNonEmpty(it.Enclosure.URL, it.Link)
		if strings.HasPrefix(link, "magnet:") {
			r.MagnetURL = link
		} else {
			r.DownloadURL = link
		}
		if t, err := time.Parse(time.RFC1123Z, it.PubDate); err == nil {
			r.PublishedAt = t
		} else if t, err := time.Parse(time.RFC1123, it.PubDate); err == nil {
			r.PublishedAt = t
		}
		for _, a := range it.Attrs {
			switch a.Name {
			case "seeders":
				r.Seeders, _ = strconv.Atoi(a.Value)
			case "peers":
				// peers counts seeders too.
				p, _ := strconv.Atoi(a.Value)
				r.Leechers = p
			case "leechers":
				r.Leechers, _ = strconv.Atoi(a.Value)
			case "grabs":
				r.Grabs, _ = strconv.Atoi(a.Value)
			case "infohash":
				r.InfoHash = strings.ToLower(a.Value)
			case "magneturl":
				r.MagnetURL = a.Value
			case "size":
				if r.Size == 0 {
					r.Size, _ = strconv.ParseInt(a.Value, 10, 64)
				}
			case "category":
				if c, err := strconv.Atoi(a.Value); err == nil {
					r.Categories = append(r.Categories, c)
				}
			case "imdb", "imdbid":
				v := strings.TrimPrefix(a.Value, "tt")
				if v != "" && v != "0" {
					for len(v) < 7 {
						v = "0" + v
					}
					r.IMDBID = "tt" + v
				}
			}
		}
		if hasPeersOnly(it) && r.Leechers >= r.Seeders {
			r.Leechers -= r.Seeders
		}
		if r.Title == "" || (r.DownloadURL == "" && r.MagnetURL == "") {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// hasPeersOnly is true when the feed gave "peers" (seeders + leechers) but
// no separate "leechers".
func hasPeersOnly(it item) bool {
	peers, leechers := false, false
	for _, a := range it.Attrs {
		peers = peers || a.Name == "peers"
		leechers = leechers || a.Name == "leechers"
	}
	return peers && !leechers
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
