package shows

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
)

// Routes adds the show routes. They need a signed-in owner.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/shows", s.handleList)
	mux.HandleFunc("GET /api/shows/lookup", s.handleLookup)
	mux.HandleFunc("POST /api/shows", s.handleAdd)
	mux.HandleFunc("GET /api/shows/{id}", s.handleGet)
	mux.HandleFunc("PUT /api/shows/{id}", s.handleChange)
	mux.HandleFunc("DELETE /api/shows/{id}", s.handleRemove)
	mux.HandleFunc("POST /api/shows/{id}/monitor", s.handleMonitor)
	mux.HandleFunc("POST /api/shows/{id}/refresh", s.handleRefresh)
	mux.HandleFunc("POST /api/shows/{id}/search", s.handleSearch)
	mux.HandleFunc("POST /api/shows/{id}/releases", s.handleReleases)
	mux.HandleFunc("POST /api/shows/{id}/grab", s.handleGrab)
	mux.HandleFunc("POST /api/shows/{id}/cancel", s.handleCancel)
}

func showID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		webserver.Error(w, http.StatusNotFound, errNotFound.Error())
		return 0, false
	}
	return id, true
}

// Summary is a show as the grid shows it: no episode list, just counts.
type Summary struct {
	TMDBID      int    `json:"tmdbId"`
	Title       string `json:"title"`
	Year        int    `json:"year"`
	PosterPath  string `json:"posterPath,omitempty"`
	Status      string `json:"status,omitempty"`
	Episodes    int    `json:"episodes"`    // followed episodes that have aired
	Have        int    `json:"have"`        // of those, downloaded
	Downloading int    `json:"downloading"` // downloads in progress
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	out := []Summary{}
	now := s.Now()
	for _, sh := range s.Store.Read().Shows {
		sum := Summary{TMDBID: sh.TMDBID, Title: sh.Title, Year: sh.Year, PosterPath: sh.PosterPath,
			Status: sh.Status, Downloading: len(sh.Downloads)}
		for _, e := range sh.Episodes {
			if e.File != nil {
				sum.Have++
				sum.Episodes++
			} else if e.Monitored && Aired(e, now) {
				sum.Episodes++
			}
		}
		out = append(out, sum)
	}
	slices.SortFunc(out, func(a, b Summary) int { return strings.Compare(sortTitle(a.Title), sortTitle(b.Title)) })
	webserver.JSON(w, http.StatusOK, out)
}

func sortTitle(t string) string {
	t = strings.ToLower(t)
	for _, a := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(t, a) {
			return t[len(a):]
		}
	}
	return t
}

func (s *Service) handleLookup(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(r.URL.Query().Get("q"))
	if text == "" {
		webserver.JSON(w, http.StatusOK, []any{})
		return
	}
	results, err := s.tmdb().SearchShows(r.Context(), text)
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
	for _, x := range results {
		_, in := s.Get(x.TMDBID)
		out = append(out, found{x.TMDBID, x.Title, x.Year, x.Overview, x.PosterPath, in})
	}
	webserver.JSON(w, http.StatusOK, out)
}

func (s *Service) handleAdd(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TMDBID  int    `json:"tmdbId"`
		Quality string `json:"quality"`
		Monitor string `json:"monitor"`
		Search  bool   `json:"search"`
	}
	if err := webserver.Decode(w, r, &body); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	sh, err := s.Add(ctx, body.TMDBID, body.Quality, body.Monitor)
	if errors.Is(err, errExists) {
		webserver.Error(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		webserver.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	if body.Search {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			if _, err := s.SearchAndGrab(ctx, sh.TMDBID, Target{}); err != nil {
				log.Printf("shows: search for %s: %v", sh.Title, err)
			}
		}()
	}
	webserver.JSON(w, http.StatusOK, map[string]int{"tmdbId": sh.TMDBID})
}

// handleGet returns the whole show, with each episode's state worked out
// for the page.
func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := showID(w, r)
	if !ok {
		return
	}
	sh, found := s.Get(id)
	if !found {
		webserver.Error(w, http.StatusNotFound, errNotFound.Error())
		return
	}
	type episodeView struct {
		store.Episode
		Aired       bool `json:"aired"`
		Downloading bool `json:"downloading"`
	}
	eps := make([]episodeView, 0, len(sh.Episodes))
	for _, e := range sh.Episodes {
		eps = append(eps, episodeView{e, Aired(e, s.Now()), downloading(sh, e)})
	}
	webserver.JSON(w, http.StatusOK, map[string]any{"show": sh, "episodes": eps, "folder": FolderName(sh)})
}

func (s *Service) handleChange(w http.ResponseWriter, r *http.Request) {
	id, ok := showID(w, r)
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
	if _, err := s.Change(id, body.Quality); err != nil {
		webserver.Error(w, http.StatusNotFound, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) handleRemove(w http.ResponseWriter, r *http.Request) {
	id, ok := showID(w, r)
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

func (s *Service) handleMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := showID(w, r)
	if !ok {
		return
	}
	var body struct {
		Season    int  `json:"season"`
		Episode   int  `json:"episode"`
		Monitored bool `json:"monitored"`
	}
	if err := webserver.Decode(w, r, &body); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.SetMonitored(id, body.Season, body.Episode, body.Monitored); err != nil {
		webserver.Error(w, http.StatusNotFound, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) handleCancel(w http.ResponseWriter, r *http.Request) {
	id, ok := showID(w, r)
	if !ok {
		return
	}
	var body struct {
		Tag string `json:"tag"`
	}
	if err := webserver.Decode(w, r, &body); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := s.Cancel(ctx, id, body.Tag); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]string{"message": "Download cancelled."})
}

func (s *Service) handleRefresh(w http.ResponseWriter, r *http.Request) {
	id, ok := showID(w, r)
	if !ok {
		return
	}
	if err := s.Refresh(r.Context(), id); err != nil {
		webserver.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	if sh, _ := s.Get(id); sh.RefreshError != "" {
		webserver.Error(w, http.StatusBadGateway, sh.RefreshError)
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]string{"message": "Episode list updated."})
}

// handleSearch searches for a season or episode, or (season 0) everything
// missing.
func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request) {
	id, ok := showID(w, r)
	if !ok {
		return
	}
	var t Target
	if err := webserver.Decode(w, r, &t); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	msg, err := s.SearchAndGrab(ctx, id, t)
	if err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]string{"message": msg})
}

func (s *Service) handleReleases(w http.ResponseWriter, r *http.Request) {
	id, ok := showID(w, r)
	if !ok {
		return
	}
	var t Target
	if err := webserver.Decode(w, r, &t); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if t.Season < 1 {
		webserver.Error(w, http.StatusBadRequest, "Choose a season.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	decisions, failures, err := s.Releases(ctx, id, t)
	if err != nil {
		webserver.Error(w, http.StatusNotFound, err.Error())
		return
	}
	if failures == nil {
		failures = []string{}
	}
	webserver.JSON(w, http.StatusOK, map[string]any{"decisions": decisions, "failures": failures})
}

// handleGrab sends a release picked by hand, even a rejected one.
func (s *Service) handleGrab(w http.ResponseWriter, r *http.Request) {
	id, ok := showID(w, r)
	if !ok {
		return
	}
	var body struct {
		Target
		Release model.Release `json:"release"`
	}
	if err := webserver.Decode(w, r, &body); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	if err := s.Grab(ctx, id, body.Target, body.Release); err != nil {
		webserver.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	_ = s.update(id, func(sh *store.Show) {
		sh.LastSearch = &store.SearchNote{At: s.Now(), Message: "Sent " + body.Release.Title + " to qBittorrent (picked by hand)."}
	})
	webserver.JSON(w, http.StatusOK, map[string]string{"message": "Sent to qBittorrent."})
}

// ActivityItems lists the show downloads in progress, for the Activity page.
func (s *Service) ActivityItems() []model.Activity {
	out := []model.Activity{}
	for _, sh := range s.Store.Read().Shows {
		for _, d := range sh.Downloads {
			out = append(out, model.Activity{
				Kind: "show", ID: sh.TMDBID, Title: sh.Title, Label: strings.ToUpper(targetOf(d).String()[:1]) + targetOf(d).String()[1:],
				PosterPath: sh.PosterPath, Download: d.Download,
			})
		}
	}
	return out
}
