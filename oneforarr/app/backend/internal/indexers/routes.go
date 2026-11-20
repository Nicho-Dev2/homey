package indexers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/webserver"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/downloads"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/indexers/cardigann"
)

// Routes adds the indexer and search routes. They need a signed-in owner.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/indexers", s.handleList)
	mux.HandleFunc("POST /api/indexers", s.handleAdd)
	mux.HandleFunc("PUT /api/indexers/{id}", s.handleChange)
	mux.HandleFunc("DELETE /api/indexers/{id}", s.handleRemove)
	mux.HandleFunc("POST /api/indexers/test", s.handleTest)
	mux.HandleFunc("GET /api/definitions", s.handleDefinitions)
	mux.HandleFunc("GET /api/definitions/{id}", s.handleDefinition)
	mux.HandleFunc("POST /api/search", s.handleSearch)
	mux.HandleFunc("POST /api/search/send", s.handleSend)
}

// View is an indexer as the browser sees it: no API key.
type View struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Definition string         `json:"definition,omitempty"`
	Name       string         `json:"name"`
	Enabled    bool           `json:"enabled"`
	URL        string         `json:"url"`
	APIKeySet  bool           `json:"apiKeySet"`
	Settings   map[string]any `json:"settings"`
}

func viewOf(ix store.Indexer) View {
	settings := ix.Settings
	if settings == nil {
		settings = map[string]any{}
	}
	return View{
		ID: ix.ID, Kind: ix.Kind, Definition: ix.Definition, Name: ix.Name, Enabled: ix.Enabled,
		URL: ix.URL, APIKeySet: ix.APIKey != "", Settings: settings,
	}
}

func (s *Service) handleList(w http.ResponseWriter, r *http.Request) {
	list := []View{}
	for _, ix := range s.Store.Read().Indexers {
		list = append(list, viewOf(ix))
	}
	webserver.JSON(w, http.StatusOK, map[string]any{
		"indexers":       list,
		"definitionsDir": s.Library.Dir,
	})
}

func (s *Service) handleAdd(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := webserver.Decode(w, r, &in); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ix, err := s.Add(in)
	if err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, viewOf(ix))
}

func (s *Service) handleChange(w http.ResponseWriter, r *http.Request) {
	var in Input
	if err := webserver.Decode(w, r, &in); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ix, err := s.Change(r.PathValue("id"), in)
	if errors.Is(err, errNotFound) {
		webserver.Error(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, viewOf(ix))
}

func (s *Service) handleRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.Remove(r.PathValue("id")); err != nil {
		webserver.Error(w, http.StatusNotFound, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleTest tests what's on the form, saved or not. With an id, an empty
// API key means the saved one.
func (s *Service) handleTest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
		Input
	}
	if err := webserver.Decode(w, r, &body); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	saved, _ := s.Get(body.ID)
	ix, err := s.build(body.Input, saved)
	if err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ix.ID = saved.ID
	n, err := s.Test(r.Context(), ix)
	if err != nil {
		webserver.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	msg := "Works: the site answered, but listed no results. It may only answer searches with words."
	if n > 0 {
		msg = "Works: the site listed " + plural(n, "result") + "."
	}
	webserver.JSON(w, http.StatusOK, map[string]string{"message": msg})
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func (s *Service) handleDefinitions(w http.ResponseWriter, r *http.Request) {
	list, err := s.Library.List()
	if err != nil {
		webserver.Error(w, http.StatusInternalServerError, "Couldn't read the definitions folder: "+err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]any{"definitions": list, "dir": s.Library.Dir})
}

// DefinitionView is one definition, with what the Add form needs.
type DefinitionView struct {
	cardigann.Summary
	Links    []string      `json:"links"`
	Settings []SettingView `json:"settings"`
}

// SettingView is one of a site's settings, for the form.
type SettingView struct {
	Name    string             `json:"name"`
	Type    string             `json:"type"`
	Label   string             `json:"label"`
	Default any                `json:"default"`
	Options []cardigann.Option `json:"options,omitempty"`
}

func (s *Service) handleDefinition(w http.ResponseWriter, r *http.Request) {
	def, err := s.Library.Get(r.PathValue("id"))
	if err != nil {
		webserver.Error(w, http.StatusNotFound, "There's no definition file with that name.")
		return
	}
	ok, why := def.Supported()
	v := DefinitionView{
		Summary: cardigann.Summary{ID: def.ID, Name: def.Name, Description: def.Description,
			Language: def.Language, Type: def.Type, Supported: ok, Reason: why},
		Links:    def.Links,
		Settings: []SettingView{},
	}
	for _, st := range def.Settings {
		v.Settings = append(v.Settings, SettingView{
			Name: st.Name, Type: st.Type, Label: labelOf(st), Default: st.DefaultValue(), Options: st.OptionList(),
		})
	}
	webserver.JSON(w, http.StatusOK, v)
}

func (s *Service) handleSearch(w http.ResponseWriter, r *http.Request) {
	var q model.Query
	if err := webserver.Decode(w, r, &q); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	q.Text = strings.TrimSpace(q.Text)
	if len(q.Text) > 200 {
		webserver.Error(w, http.StatusBadRequest, "Use fewer words.")
		return
	}
	switch q.Kind {
	case "search", "movie", "tv":
	case "":
		q.Kind = "search"
	default:
		webserver.Error(w, http.StatusBadRequest, "Unknown kind of search.")
		return
	}
	if len(s.Store.Read().Indexers) == 0 {
		webserver.Error(w, http.StatusBadRequest, "Add an indexer first.")
		return
	}
	webserver.JSON(w, http.StatusOK, s.Search(r.Context(), q))
}

// handleSend gives one search result to qBittorrent.
func (s *Service) handleSend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IndexerID   string `json:"indexerId"`
		DownloadURL string `json:"downloadUrl"`
		MagnetURL   string `json:"magnetUrl"`
		Size        int64  `json:"size"`
	}
	if err := webserver.Decode(w, r, &body); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	link, err := s.DownloadLink(ctx, body.IndexerID, body.DownloadURL, body.MagnetURL)
	if err != nil {
		webserver.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	if err := downloads.Send(ctx, s.Store.Read().Settings, link, "", body.Size); err != nil {
		webserver.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]string{"message": "Sent to qBittorrent."})
}
