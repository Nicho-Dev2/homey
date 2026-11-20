// Package metadata looks up movie and show details.
//
//   - TMDB (themoviedb.org) for movies, shows and posters. Free for
//     non-commercial use with a free key; TMDB must be credited in the app.
//   - TVmaze (tvmaze.com) for episode lists and air dates. Free, no key,
//     data licensed CC BY-SA. (Added with Shows.)
//
// Answers are saved locally, so each service is asked rarely.
package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// TMDBBase is TMDB's API address. Tests replace it.
var TMDBBase = "https://api.themoviedb.org/3"

// TMDB is a client for TMDB's API (v3).
type TMDB struct {
	key  string
	http *http.Client
}

// NewTMDB returns a TMDB client. key is either the short "API key" or the
// long "API Read Access Token"; TMDB's settings page shows both.
func NewTMDB(key string) *TMDB {
	return &TMDB{key: strings.TrimSpace(key), http: &http.Client{Timeout: 15 * time.Second}}
}

// Check makes one small request to confirm the key works.
func (t *TMDB) Check(ctx context.Context) error {
	if t.key == "" {
		return errors.New("Enter a TMDB key first.")
	}
	_, err := t.get(ctx, "/configuration")
	return err
}

func (t *TMDB) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, TMDBBase+path, nil)
	if err != nil {
		return nil, err
	}
	if isReadToken(t.key) {
		req.Header.Set("Authorization", "Bearer "+t.key)
	} else {
		q := req.URL.Query()
		q.Set("api_key", t.key)
		req.URL.RawQuery = q.Encode()
	}
	if t.key == "" {
		return nil, errors.New("Add a TMDB key in Settings first.")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return nil, errors.New("Couldn't reach TMDB. Check the internet connection.")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("reading TMDB's answer: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return body, nil
	case http.StatusUnauthorized:
		return nil, errors.New("TMDB refused the key. Copy it again from TMDB's API settings page.")
	case http.StatusTooManyRequests:
		return nil, errors.New("TMDB says we're asking too often. Try again in a minute.")
	default:
		return nil, fmt.Errorf("TMDB answered %d.", resp.StatusCode)
	}
}

// isReadToken tells the long read access token (a JWT, which always starts
// with "eyJ") from the 32-character API key.
func isReadToken(key string) bool { return strings.HasPrefix(key, "eyJ") }

// MovieResult is one movie in TMDB's search results.
type MovieResult struct {
	TMDBID        int    `json:"tmdbId"`
	Title         string `json:"title"`
	OriginalTitle string `json:"originalTitle"`
	Year          int    `json:"year"`
	Overview      string `json:"overview"`
	PosterPath    string `json:"posterPath"`
}

// Movie is one movie's details.
type Movie struct {
	MovieResult
	IMDBID       string `json:"imdbId"`
	Runtime      int    `json:"runtime"` // minutes
	BackdropPath string `json:"backdropPath"`
	Status       string `json:"status"` // Released, In Production...
	ReleaseDate  string `json:"releaseDate"`
}

type tmdbMovie struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	ReleaseDate   string `json:"release_date"`
	Overview      string `json:"overview"`
	PosterPath    string `json:"poster_path"`
	BackdropPath  string `json:"backdrop_path"`
	IMDBID        string `json:"imdb_id"`
	Runtime       int    `json:"runtime"`
	Status        string `json:"status"`
}

func (m tmdbMovie) result() MovieResult {
	year := 0
	if len(m.ReleaseDate) >= 4 {
		year, _ = strconv.Atoi(m.ReleaseDate[:4])
	}
	return MovieResult{TMDBID: m.ID, Title: m.Title, OriginalTitle: m.OriginalTitle, Year: year,
		Overview: m.Overview, PosterPath: m.PosterPath}
}

// SearchMovies looks movies up by title. A year in the text ("Alien 1979")
// is used to narrow the search.
func (t *TMDB) SearchMovies(ctx context.Context, text string) ([]MovieResult, error) {
	q := url.Values{"query": {strings.TrimSpace(text)}, "include_adult": {"false"}}
	if m := reTrailingYear.FindStringSubmatch(text); m != nil {
		q.Set("query", strings.TrimSpace(m[1]))
		q.Set("year", m[2])
	}
	body, err := t.get(ctx, "/search/movie?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var page struct {
		Results []tmdbMovie `json:"results"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, errors.New("TMDB's answer couldn't be read.")
	}
	out := make([]MovieResult, 0, len(page.Results))
	for _, m := range page.Results {
		out = append(out, m.result())
	}
	return out, nil
}

var reTrailingYear = regexp.MustCompile(`^(.+?)\s+\(?((?:19|20)\d\d)\)?$`)

// Movie returns one movie's details.
func (t *TMDB) Movie(ctx context.Context, id int) (Movie, error) {
	body, err := t.get(ctx, "/movie/"+strconv.Itoa(id))
	if err != nil {
		return Movie{}, err
	}
	var m tmdbMovie
	if err := json.Unmarshal(body, &m); err != nil || m.ID == 0 {
		return Movie{}, errors.New("TMDB's answer couldn't be read.")
	}
	return Movie{MovieResult: m.result(), IMDBID: m.IMDBID, Runtime: m.Runtime,
		BackdropPath: m.BackdropPath, Status: m.Status, ReleaseDate: m.ReleaseDate}, nil
}

// ShowResult is one show in TMDB's search results.
type ShowResult struct {
	TMDBID       int    `json:"tmdbId"`
	Title        string `json:"title"`
	OriginalName string `json:"originalTitle"`
	Year         int    `json:"year"` // first aired
	Overview     string `json:"overview"`
	PosterPath   string `json:"posterPath"`
}

// Show is one show's details from TMDB, with the ids used to find its
// episodes on TVmaze.
type Show struct {
	ShowResult
	TVDBID       int    `json:"tvdbId"`
	IMDBID       string `json:"imdbId"`
	BackdropPath string `json:"backdropPath"`
	Status       string `json:"status"` // Returning Series, Ended...
}

type tmdbShow struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	OriginalName string `json:"original_name"`
	FirstAirDate string `json:"first_air_date"`
	Overview     string `json:"overview"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
	Status       string `json:"status"`
	ExternalIDs  struct {
		TVDBID int    `json:"tvdb_id"`
		IMDBID string `json:"imdb_id"`
	} `json:"external_ids"`
}

func (s tmdbShow) result() ShowResult {
	year := 0
	if len(s.FirstAirDate) >= 4 {
		year, _ = strconv.Atoi(s.FirstAirDate[:4])
	}
	return ShowResult{TMDBID: s.ID, Title: s.Name, OriginalName: s.OriginalName, Year: year,
		Overview: s.Overview, PosterPath: s.PosterPath}
}

// SearchShows looks shows up by title.
func (t *TMDB) SearchShows(ctx context.Context, text string) ([]ShowResult, error) {
	q := url.Values{"query": {strings.TrimSpace(text)}, "include_adult": {"false"}}
	if m := reTrailingYear.FindStringSubmatch(text); m != nil {
		q.Set("query", strings.TrimSpace(m[1]))
		q.Set("first_air_date_year", m[2])
	}
	body, err := t.get(ctx, "/search/tv?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var page struct {
		Results []tmdbShow `json:"results"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, errors.New("TMDB's answer couldn't be read.")
	}
	out := make([]ShowResult, 0, len(page.Results))
	for _, s := range page.Results {
		out = append(out, s.result())
	}
	return out, nil
}

// Show returns one show's details, with its TVDB and IMDb ids.
func (t *TMDB) Show(ctx context.Context, id int) (Show, error) {
	body, err := t.get(ctx, "/tv/"+strconv.Itoa(id)+"?append_to_response=external_ids")
	if err != nil {
		return Show{}, err
	}
	var s tmdbShow
	if err := json.Unmarshal(body, &s); err != nil || s.ID == 0 {
		return Show{}, errors.New("TMDB's answer couldn't be read.")
	}
	return Show{ShowResult: s.result(), TVDBID: s.ExternalIDs.TVDBID, IMDBID: s.ExternalIDs.IMDBID,
		BackdropPath: s.BackdropPath, Status: s.Status}, nil
}
