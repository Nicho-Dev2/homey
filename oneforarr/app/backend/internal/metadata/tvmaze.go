package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TVmazeBase is TVmaze's API address. Tests replace it.
var TVmazeBase = "https://api.tvmaze.com"

// TVmaze is a client for TVmaze's public API: free, no key, data licensed
// CC BY-SA. It allows at least 20 requests every 10 seconds; the client
// keeps well under that.
type TVmaze struct {
	http *http.Client

	mu   sync.Mutex
	last time.Time
}

// NewTVmaze returns a TVmaze client.
func NewTVmaze() *TVmaze { return &TVmaze{http: &http.Client{Timeout: 20 * time.Second}} }

// minGap keeps requests at no more than 4 a second.
const minGap = 250 * time.Millisecond

// Episode is one episode as TVmaze lists it.
type Episode struct {
	Season   int       `json:"season"`
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	AirDate  string    `json:"airDate"`  // YYYY-MM-DD; "" when not announced
	AirStamp time.Time `json:"airStamp"` // exact time, when known
	Runtime  int       `json:"runtime"`
	Overview string    `json:"overview"`
}

// FindShow returns TVmaze's id for a show, looked up by its TVDB id, then
// its IMDb id.
func (t *TVmaze) FindShow(ctx context.Context, tvdbID int, imdbID string) (int, error) {
	var tries []url.Values
	if tvdbID > 0 {
		tries = append(tries, url.Values{"thetvdb": {strconv.Itoa(tvdbID)}})
	}
	if imdbID != "" {
		tries = append(tries, url.Values{"imdb": {imdbID}})
	}
	for _, q := range tries {
		body, status, err := t.get(ctx, "/lookup/shows?"+q.Encode())
		if err != nil {
			return 0, err
		}
		if status == http.StatusNotFound {
			continue
		}
		var show struct {
			ID int `json:"id"`
		}
		if json.Unmarshal(body, &show) == nil && show.ID > 0 {
			return show.ID, nil
		}
	}
	return 0, errors.New("TVmaze doesn't list this show, so its episodes aren't known.")
}

// Episodes lists a show's episodes (without specials), in order.
func (t *TVmaze) Episodes(ctx context.Context, tvmazeID int) ([]Episode, error) {
	body, status, err := t.get(ctx, "/shows/"+strconv.Itoa(tvmazeID)+"/episodes")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("TVmaze answered %d for the episode list.", status)
	}
	var raw []struct {
		Season   int    `json:"season"`
		Number   *int   `json:"number"`
		Name     string `json:"name"`
		AirDate  string `json:"airdate"`
		AirStamp string `json:"airstamp"`
		Runtime  int    `json:"runtime"`
		Summary  string `json:"summary"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, errors.New("TVmaze's episode list couldn't be read.")
	}
	out := make([]Episode, 0, len(raw))
	for _, e := range raw {
		if e.Number == nil || e.Season < 1 {
			continue // specials have no number
		}
		ep := Episode{Season: e.Season, Number: *e.Number, Title: e.Name, AirDate: e.AirDate,
			Runtime: e.Runtime, Overview: stripHTML(e.Summary)}
		ep.AirStamp, _ = time.Parse(time.RFC3339, e.AirStamp)
		out = append(out, ep)
	}
	return out, nil
}

func (t *TVmaze) get(ctx context.Context, path string) ([]byte, int, error) {
	t.mu.Lock()
	if d := time.Until(t.last.Add(minGap)); d > 0 {
		time.Sleep(d)
	}
	t.last = time.Now()
	t.mu.Unlock()

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, TVmazeBase+path, nil)
		if err != nil {
			return nil, 0, err
		}
		resp, err := t.http.Do(req)
		if err != nil {
			return nil, 0, errors.New("Couldn't reach TVmaze. Check the internet connection.")
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		resp.Body.Close()
		if err != nil {
			return nil, 0, errors.New("Reading TVmaze's answer failed.")
		}
		// 429: too many requests. Wait a little and try again, twice.
		if resp.StatusCode == http.StatusTooManyRequests && attempt < 2 {
			select {
			case <-time.After(3 * time.Second):
				continue
			case <-ctx.Done():
				return nil, 0, ctx.Err()
			}
		}
		return body, resp.StatusCode, nil
	}
}

var reTags = regexp.MustCompile(`<[^>]*>`)

// stripHTML turns TVmaze's "<p>Text</p>" summaries into plain text.
func stripHTML(s string) string {
	return strings.TrimSpace(html.UnescapeString(reTags.ReplaceAllString(s, "")))
}
