package movies

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/webserver"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
)

// Routes adds the movie routes. They need a signed-in owner.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/movies", s.handleList)
	mux.HandleFunc("GET /api/movies/lookup", s.handleLookup)
	mux.HandleFunc("GET /api/movies/presets", s.handlePresets)
	mux.HandleFunc("POST /api/movies", s.handleAdd)
	mux.HandleFunc("GET /api/movies/{id}", s.handleGet)
	mux.HandleFunc("PUT /api/movies/{id}", s.handleChange)
	mux.HandleFunc("DELETE /api/movies/{id}", s.handleRemove)
	mux.HandleFunc("POST /api/movies/{id}/search", s.handleSearch)
	mux.HandleFunc("POST /api/movies/{id}/releases", s.handleReleases)
	mux.HandleFunc("POST /api/movies/{id}/grab", s.handleGrab)
	mux.HandleFunc("POST /api/movies/{id}/cancel", s.handleCancel)
}

func movieID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		webserver.Error(w, http.StatusNotFound, errNotFound.Error())
		return 0, false
	}
	return id, true
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	list := slices.Clone(s.Store.Read().Movies)
	if list == nil {
		list = []store.Movie{}
	}
	slices.SortFunc(list, func(a, b store.Movie) int {
		return strings.Compare(sortTitle(a.Title), sortTitle(b.Title))
	})
	webserver.JSON(w, http.StatusOK, list)
}

// sortTitle sorts "The Thing" under T for Thing.
func sortTitle(t string) string {
	t = strings.ToLower(t)
	for _, a := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(t, a) {
			return t[len(a):]
		}
	}
	return t
}

func (s *Service) handlePresets(w http.ResponseWriter, r *http.Request) {
	webserver.JSON(w, http.StatusOK, library.Presets)
}

// handleLookup searches TMDB and marks movies already in the library.
func (s *Service) handleLookup(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(r.URL.Query().Get("q"))
	if text == "" {
		webserver.JSON(w, http.StatusOK, []any{})
		return
	}
	results, err := s.tmdb().SearchMovies(r.Context(), text)
	if err != nil {
		webserver.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	type found struct {
		TMDBID     int    `json:"tmdbId"`
		Title      string `json:"title"`
		Year       int    `json:"year"`
		Overview   string `json:"overview"`
		PosterPath string `json:"posterPath"`
		InLibrary  bool   `json:"inLibrary"`
	}
	out := make([]found, 0, len(results))
	for _, m := range results {
		_, in := s.Get(m.TMDBID)
		out = append(out, found{m.TMDBID, m.Title, m.Year, m.Overview, m.PosterPath, in})
	}
	webserver.JSON(w, http.StatusOK, out)
}

func (s *Service) handleAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TMDBID  int    `json:"tmdbId"`
		Quality string `json:"quality"`
		Search  bool   `json:"search"`
	}
	if err := webserver.Decode(w, r, &body); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	m, err := s.Add(r.Context(), body.TMDBID, body.Quality)
	if errors.Is(err, errExists) {
		webserver.Error(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		webserver.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	if body.Search && m.File == nil {
		// Searching can take a while; the movie page shows the outcome.
		go s.searchInBackground(m.TMDBID)
	}
	webserver.JSON(w, http.StatusOK, m)
}

func (s *Service) searchInBackground(id int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if msg, err := s.SearchAndGrab(ctx, id); err != nil {
		log.Printf("movies: search for %d: %s %v", id, msg, err)
	}
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := movieID(w, r)
	if !ok {
		return
	}
	m, found := s.Get(id)
	if !found {
		webserver.Error(w, http.StatusNotFound, errNotFound.Error())
		return
	}
	// upgradable: in the library, below the quality wanted, upgrades on, and
	// nothing downloading. The page shows "Look for Upgrade" only then.
	upgradable := m.File != nil && s.Wanted(m)
	webserver.JSON(w, http.StatusOK, map[string]any{"movie": m, "folder": FolderName(m), "upgradable": upgradable})
}

func (s *Service) handleChange(w http.ResponseWriter, r *http.Request) {
	id, ok := movieID(w, r)
	if !ok {
		return
	}
	var body struct {
		Quality string `json:"quality"`
	}
	if err := webserver.Decode(w, r, &body); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	m, err := s.Change(id, body.Quality)
	if err != nil {
		webserver.Error(w, http.StatusNotFound, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, m)
}

func (s *Service) handleRemove(w http.ResponseWriter, r *http.Request) {
	id, ok := movieID(w, r)
	if !ok {
		return
	}
	var body struct {
		DeleteFiles bool `json:"deleteFiles"`
	}
	if err := webserver.Decode(w, r, &body); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Remove(id, body.DeleteFiles); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request) {
	id, ok := movieID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	msg, err := s.SearchAndGrab(ctx, id)
	if err != nil && msg == "" {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		webserver.Error(w, http.StatusBadGateway, msg)
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]string{"message": msg})
}

func (s *Service) handleCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := movieID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.Cancel(ctx, id); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]string{"message": "Download cancelled."})
}

func (s *Service) handleReleases(w http.ResponseWriter, r *http.Request) {
	id, ok := movieID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	decisions, failures, err := s.Releases(ctx, id, r.URL.Query().Get("replace") == "1")
	if err != nil {
		webserver.Error(w, http.StatusNotFound, err.Error())
		return
	}
	if failures == nil {
		failures = []string{}
	}
	webserver.JSON(w, http.StatusOK, map[string]any{"decisions": decisions, "failures": failures})
}

// handleGrab sends a release picked by hand on the Find releases page,
// even one the app rejected.
func (s *Service) handleGrab(w http.ResponseWriter, r *http.Request) {
	id, ok := movieID(w, r)
	if !ok {
		return
	}
	var rel model.Release
	if err := webserver.Decode(w, r, &rel); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := s.Grab(ctx, id, rel); err != nil {
		webserver.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	replacing := r.URL.Query().Get("replace") == "1"
	_ = s.update(id, func(m *store.Movie) {
		if m.Download != nil {
			m.Download.Replace = replacing && m.File != nil
		}
		m.LastSearch = &store.SearchNote{At: s.Now(), Message: "Sent " + rel.Title + " to qBittorrent (picked by hand)."}
	})
	webserver.JSON(w, http.StatusOK, map[string]string{"message": "Sent to qBittorrent."})
}

// ActivityItems lists the movie downloads in progress, for the Activity page.
func (s *Service) ActivityItems() []model.Activity {
	out := []model.Activity{}
	for _, m := range s.Store.Read().Movies {
		if m.Download != nil {
			out = append(out, model.Activity{Kind: "movie", ID: m.TMDBID, Title: m.Title, Year: m.Year,
				PosterPath: m.PosterPath, Download: *m.Download})
		}
	}
	return out
}
