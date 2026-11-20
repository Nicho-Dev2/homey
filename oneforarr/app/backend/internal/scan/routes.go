package scan

import (
	"net/http"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/webserver"
)

// Routes adds the Library page's API:
//
//	GET  /api/library          last scan, unmatched and skipped folders
//	POST /api/library/scan     start a scan (it runs in the background)
//	POST /api/library/match    {path, tmdbId}: add an unmatched folder
//	POST /api/library/skip     {path}: leave it alone from now on
//	POST /api/library/unskip   {path}
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/library", s.handleGet)
	mux.HandleFunc("POST /api/library/scan", s.handleScan)
	mux.HandleFunc("POST /api/library/match", s.handleMatch)
	mux.HandleFunc("POST /api/library/skip", s.handleSkip)
	mux.HandleFunc("POST /api/library/unskip", s.handleUnskip)
}

type view struct {
	store.Library
	Scanning bool `json:"scanning"`
}

func (s *Service) view() view {
	lib := s.Store.Read().Library
	if lib.Unmatched == nil {
		lib.Unmatched = []store.Unmatched{}
	}
	if lib.Skipped == nil {
		lib.Skipped = []string{}
	}
	return view{Library: lib, Scanning: s.Running()}
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	webserver.JSON(w, http.StatusOK, s.view())
}

func (s *Service) handleScan(w http.ResponseWriter, r *http.Request) {
	s.Start()
	webserver.JSON(w, http.StatusAccepted, s.view())
}

type pathBody struct {
	Path   string `json:"path"`
	TMDBID int    `json:"tmdbId"`
}

func (s *Service) handleMatch(w http.ResponseWriter, r *http.Request) {
	var b pathBody
	if err := webserver.Decode(w, r, &b); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	title, err := s.Match(r.Context(), b.Path, b.TMDBID)
	if err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]any{"message": "Added " + title + ".", "library": s.view()})
}

func (s *Service) handleSkip(w http.ResponseWriter, r *http.Request) {
	var b pathBody
	if err := webserver.Decode(w, r, &b); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Skip(b.Path); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, s.view())
}

func (s *Service) handleUnskip(w http.ResponseWriter, r *http.Request) {
	var b pathBody
	if err := webserver.Decode(w, r, &b); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Unskip(b.Path)
	webserver.JSON(w, http.StatusOK, s.view())
}
