package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// Title is one movie or show in a TMDB list (trending, discover).
type Title struct {
	Kind         string  `json:"kind"` // "movie" or "show"
	TMDBID       int     `json:"tmdbId"`
	Title        string  `json:"title"`
	Year         int     `json:"year"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"posterPath"`
	BackdropPath string  `json:"backdropPath"`
	Rating       float64 `json:"rating"` // TMDB's average, 0–10
	Votes        int     `json:"votes"`
}

type tmdbListItem struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"` // movies
	Name         string  `json:"name"`  // shows
	ReleaseDate  string  `json:"release_date"`
	FirstAirDate string  `json:"first_air_date"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	VoteAverage  float64 `json:"vote_average"`
	VoteCount    int     `json:"vote_count"`
	Adult        bool    `json:"adult"`
}

// List reads one page of a TMDB list of movies (kind "movie") or shows
// (kind "show"), e.g. "/trending/movie/week" or "/discover/tv".
func (t *TMDB) List(ctx context.Context, kind, path string, q url.Values) ([]Title, error) {
	if q == nil {
		q = url.Values{}
	}
	q.Set("include_adult", "false")
	body, err := t.get(ctx, path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var page struct {
		Results []tmdbListItem `json:"results"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, errors.New("TMDB's answer couldn't be read.")
	}
	out := make([]Title, 0, len(page.Results))
	for _, r := range page.Results {
		if r.Adult || r.PosterPath == "" {
			continue
		}
		it := Title{Kind: kind, TMDBID: r.ID, Title: r.Title, Overview: r.Overview, PosterPath: r.PosterPath,
			BackdropPath: r.BackdropPath, Rating: r.VoteAverage, Votes: r.VoteCount}
		date := r.ReleaseDate
		if kind == "show" {
			it.Title, date = r.Name, r.FirstAirDate
		}
		if len(date) >= 4 {
			it.Year, _ = strconv.Atoi(date[:4])
		}
		out = append(out, it)
	}
	return out, nil
}

// Provider is a streaming service in TMDB's watch-provider data (which
// comes from JustWatch).
type Provider struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	LogoPath string `json:"logoPath"`
	priority int
}

// Providers lists the streaming services for movies in a country, most
// prominent first, leaving out ad-supported tiers and add-on channels
// (they carry the same titles as the main service).
func (t *TMDB) Providers(ctx context.Context, region string) ([]Provider, error) {
	body, err := t.get(ctx, "/watch/providers/movie?"+url.Values{"watch_region": {region}}.Encode())
	if err != nil {
		return nil, err
	}
	var page struct {
		Results []struct {
			ID         int            `json:"provider_id"`
			Name       string         `json:"provider_name"`
			LogoPath   string         `json:"logo_path"`
			Priorities map[string]int `json:"display_priorities"`
			Priority   int            `json:"display_priority"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, errors.New("TMDB's streaming services couldn't be read.")
	}
	var out []Provider
	for _, r := range page.Results {
		low := strings.ToLower(r.Name)
		if strings.Contains(low, "with ads") || strings.Contains(low, "channel") || strings.Contains(low, "standard") {
			continue
		}
		p := Provider{ID: r.ID, Name: r.Name, LogoPath: r.LogoPath, priority: r.Priority}
		if v, ok := r.Priorities[region]; ok {
			p.priority = v
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].priority < out[j].priority })
	return out, nil
}
