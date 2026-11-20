// Package settings serves the Settings page: reading and saving settings,
// and the Test buttons for qBittorrent, TMDB and Jellyfin.
//
// Passwords and keys are never sent back to the browser. The page gets
// "passwordSet: true" instead, and sending an empty value keeps the saved one.
package settings

import (
	"context"
	"errors"
	"math"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/webserver"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/downloads"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/jellyfin"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/metadata"
)

// Service is the settings part of the app.
type Service struct {
	Store *store.Store
}

// Routes adds the settings routes. They need a signed-in owner.
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings", s.handleGet)
	mux.HandleFunc("PUT /api/settings", s.handlePut)
	mux.HandleFunc("POST /api/settings/test/qbittorrent", s.handleTestQBittorrent)
	mux.HandleFunc("POST /api/settings/test/tmdb", s.handleTestTMDB)
	mux.HandleFunc("POST /api/settings/test/jellyfin", s.handleTestJellyfin)
}

// View is the settings as the browser sees them: no secrets.
type View struct {
	QBittorrent QBittorrentView  `json:"qbittorrent"`
	Metadata    MetadataView     `json:"metadata"`
	Folders     store.Folders    `json:"folders"`
	Movies      store.MovieRules `json:"movies"`
	Shows       store.ShowRules  `json:"shows"`
	Schedule    store.Schedule   `json:"schedule"`
	Jellyfin    JellyfinView     `json:"jellyfin"`
	// FreeGB is the free space on the downloads drive now (-1 = unknown).
	FreeGB float64 `json:"freeGB"`
}

type JellyfinView struct {
	URL       string `json:"url"`
	APIKeySet bool   `json:"apiKeySet"`
}

type QBittorrentView struct {
	URL         string `json:"url"`
	Username    string `json:"username"`
	PasswordSet bool   `json:"passwordSet"`
	Category    string `json:"category"`
	// RestartWhenStuck and GiveUpHours: see store.QBittorrent.
	RestartWhenStuck bool `json:"restartWhenStuck"`
	GiveUpHours      int  `json:"giveUpHours"`
}

type MetadataView struct {
	TMDBKeySet bool   `json:"tmdbKeySet"`
	Region     string `json:"region"`
}

// Change is what the browser sends to save. An empty Password, TMDBKey or
// Jellyfin APIKey keeps the saved value.
type Change struct {
	QBittorrent struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
		Category string `json:"category"`

		RestartWhenStuck bool `json:"restartWhenStuck"`
		GiveUpHours      int  `json:"giveUpHours"`
	} `json:"qbittorrent"`
	Metadata struct {
		TMDBKey string `json:"tmdbKey"`
		Region  string `json:"region"`
	} `json:"metadata"`
	Folders  store.Folders    `json:"folders"`
	Movies   store.MovieRules `json:"movies"`
	Shows    store.ShowRules  `json:"shows"`
	Schedule store.Schedule   `json:"schedule"`
	Jellyfin struct {
		URL    string `json:"url"`
		APIKey string `json:"apiKey"`
	} `json:"jellyfin"`
}

func viewOf(st store.Settings) View {
	v := View{
		QBittorrent: QBittorrentView{
			URL:         st.QBittorrent.URL,
			Username:    st.QBittorrent.Username,
			PasswordSet: st.QBittorrent.Password != "",
			Category:    st.QBittorrent.Category,

			RestartWhenStuck: st.QBittorrent.RestartWhenStuck,
			GiveUpHours:      st.QBittorrent.GiveUpHours,
		},
		Metadata: MetadataView{TMDBKeySet: st.Metadata.TMDBKey != "", Region: st.Metadata.Region},
		Folders:  st.Folders,
		Movies:   st.Movies,
		Shows:    st.Shows,
		Schedule: st.Schedule,
		Jellyfin: JellyfinView{URL: st.Jellyfin.URL, APIKeySet: st.Jellyfin.APIKey != ""},
		FreeGB:   -1,
	}
	if free, err := downloads.FreeBytes(st.Folders.Downloads); err == nil {
		v.FreeGB = math.Round(float64(free)/(1<<30)*10) / 10
	}
	return v
}

// apply returns saved with the change applied, or an error to show.
func apply(saved store.Settings, c Change) (store.Settings, error) {
	next := saved
	next.QBittorrent.URL = strings.TrimSpace(c.QBittorrent.URL)
	next.QBittorrent.Username = strings.TrimSpace(c.QBittorrent.Username)
	if c.QBittorrent.Password != "" {
		next.QBittorrent.Password = c.QBittorrent.Password
	}
	next.QBittorrent.Category = strings.TrimSpace(c.QBittorrent.Category)
	next.QBittorrent.RestartWhenStuck = c.QBittorrent.RestartWhenStuck
	next.QBittorrent.GiveUpHours = c.QBittorrent.GiveUpHours
	if region := strings.ToUpper(strings.TrimSpace(c.Metadata.Region)); region != "" {
		next.Metadata.Region = region
	}
	if c.Metadata.TMDBKey != "" {
		next.Metadata.TMDBKey = strings.TrimSpace(c.Metadata.TMDBKey)
	}
	next.Folders = store.Folders{
		Movies:    cleanFolder(c.Folders.Movies),
		Shows:     cleanFolder(c.Folders.Shows),
		Downloads: cleanFolder(c.Folders.Downloads),
		QBitRoot:  cleanFolder(c.Folders.QBitRoot),
		AppRoot:   cleanFolder(c.Folders.AppRoot),
		MinFreeGB: c.Folders.MinFreeGB,
	}
	next.Movies = c.Movies
	next.Shows = c.Shows
	next.Schedule = c.Schedule
	next.Jellyfin.URL = strings.TrimRight(strings.TrimSpace(c.Jellyfin.URL), "/")
	if c.Jellyfin.APIKey != "" {
		next.Jellyfin.APIKey = strings.TrimSpace(c.Jellyfin.APIKey)
	}

	if next.QBittorrent.Category == "" {
		return saved, errors.New("Give the qBittorrent category a name, e.g. oneforarr.")
	}
	for name, f := range map[string]string{"Movies": next.Folders.Movies, "Shows": next.Folders.Shows, "Downloads": next.Folders.Downloads,
		"qBittorrent's drive": next.Folders.QBitRoot, "This app's drive": next.Folders.AppRoot} {
		if !strings.HasPrefix(f, "/") {
			return saved, errors.New("The " + name + " folder must be a full path, e.g. /data/movies.")
		}
	}
	if !library.ValidPreset(next.Movies.Quality) || !library.ValidPreset(next.Shows.Quality) {
		return saved, errors.New("Choose a quality for new movies and shows.")
	}
	if next.Movies.MaxSizeGB < 0 || next.Movies.MinSeeders < 0 || next.Shows.MaxEpisodeGB < 0 || next.Shows.MinSeeders < 0 {
		return saved, errors.New("The size limit and seeders can't be negative.")
	}
	if next.QBittorrent.GiveUpHours < 0 {
		return saved, errors.New("The hours before giving up can't be negative (0 = never).")
	}
	if next.Folders.MinFreeGB < 0 {
		return saved, errors.New("The space to keep free can't be negative (0 turns the limit off).")
	}
	if next.Metadata.Region != "" && len(next.Metadata.Region) != 2 {
		return saved, errors.New("The country must be a two-letter code, like US or GB.")
	}
	if next.Schedule.RSSMinutes < 0 || next.Schedule.SearchHours < 0 {
		return saved, errors.New("The automatic intervals can't be negative (0 turns one off).")
	}
	if next.Schedule.RSSMinutes > 0 && next.Schedule.RSSMinutes < 5 {
		return saved, errors.New("Check the newest releases every 5 minutes at most, so sites don't block you.")
	}
	if next.Jellyfin.URL != "" {
		if _, err := jellyfin.New(next.Jellyfin.URL, next.Jellyfin.APIKey); err != nil {
			return saved, err
		}
	}
	return next, nil
}

func cleanFolder(f string) string {
	f = strings.TrimSpace(f)
	if f == "" {
		return ""
	}
	return path.Clean(f)
}

func (s *Service) handleGet(w http.ResponseWriter, r *http.Request) {
	webserver.JSON(w, http.StatusOK, viewOf(s.Store.Read().Settings))
}

func (s *Service) handlePut(w http.ResponseWriter, r *http.Request) {
	var c Change
	if err := webserver.Decode(w, r, &c); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	var saved store.Settings
	err := s.Store.Update(func(d *store.Data) error {
		next, err := apply(d.Settings, c)
		if err != nil {
			return err
		}
		d.Settings = next
		saved = next
		return nil
	})
	if err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, viewOf(saved))
}

// The Test buttons test what's on the page, saved or not, so the owner can
// check before saving. An empty secret means "use the saved one".

func (s *Service) handleTestQBittorrent(w http.ResponseWriter, r *http.Request) {
	var c Change
	if err := webserver.Decode(w, r, &c); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	password := c.QBittorrent.Password
	if password == "" {
		password = s.Store.Read().Settings.QBittorrent.Password
	}
	client, err := downloads.NewQBittorrent(c.QBittorrent.URL, c.QBittorrent.Username, password)
	if err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	version, err := client.Version(ctx)
	if err != nil {
		webserver.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]string{"message": "Connected to qBittorrent " + version + "."})
}

func (s *Service) handleTestTMDB(w http.ResponseWriter, r *http.Request) {
	var c Change
	if err := webserver.Decode(w, r, &c); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	key := c.Metadata.TMDBKey
	if key == "" {
		key = s.Store.Read().Settings.Metadata.TMDBKey
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := metadata.NewTMDB(key).Check(ctx); err != nil {
		webserver.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]string{"message": "TMDB accepted the key."})
}

func (s *Service) handleTestJellyfin(w http.ResponseWriter, r *http.Request) {
	var c Change
	if err := webserver.Decode(w, r, &c); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	key := c.Jellyfin.APIKey
	if key == "" {
		key = s.Store.Read().Settings.Jellyfin.APIKey
	}
	client, err := jellyfin.New(c.Jellyfin.URL, key)
	if err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	name, err := client.Check(ctx)
	if err != nil {
		webserver.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	webserver.JSON(w, http.StatusOK, map[string]string{"message": "Connected to " + name + "."})
}
