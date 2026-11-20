package settings

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
)

func TestSecretsNeverSentAndKeptWhenEmpty(t *testing.T) {
	db, _ := store.Open(filepath.Join(t.TempDir(), "data.json"))
	_ = db.Update(func(d *store.Data) error {
		d.Settings.QBittorrent.Password = "hunter2hunter2"
		d.Settings.Metadata.TMDBKey = "tmdbkey"
		d.Settings.Jellyfin.APIKey = "jfkeyjfkey"
		return nil
	})
	s := &Service{Store: db}
	mux := http.NewServeMux()
	s.Routes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/settings", nil))
	if strings.Contains(rec.Body.String(), "hunter2") || strings.Contains(rec.Body.String(), "tmdbkey") || strings.Contains(rec.Body.String(), "jfkey") {
		t.Fatalf("secret sent to browser: %s", rec.Body)
	}
	var v View
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if !v.QBittorrent.PasswordSet || !v.Metadata.TMDBKeySet || !v.Jellyfin.APIKeySet {
		t.Fatalf("set flags missing: %+v", v)
	}

	// Saving with empty secrets keeps them.
	body := `{"qbittorrent":{"url":"http://gluetun:8080","username":"admin","password":"","category":"oneforarr"},
		"metadata":{"tmdbKey":""},"folders":{"movies":"/data/movies/","shows":"/data/tv","downloads":"/data/downloads","qbitRoot":"/data","appRoot":"/Volumes/M/"},
		"movies":{"quality":"2160p","maxSizeGB":40,"minSeeders":2},"shows":{"quality":"720p","maxEpisodeGB":3,"minSeeders":1},
		"schedule":{"rssMinutes":30,"searchHours":0},"jellyfin":{"url":"http://localhost:8096/","apiKey":""}}`
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/settings", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	saved := db.Read().Settings
	if saved.QBittorrent.Password != "hunter2hunter2" || saved.Metadata.TMDBKey != "tmdbkey" || saved.Jellyfin.APIKey != "jfkeyjfkey" {
		t.Fatalf("secrets lost: %+v", saved)
	}
	if saved.Folders.Movies != "/data/movies" || saved.Folders.AppRoot != "/Volumes/M" {
		t.Errorf("folder not cleaned: %+v", saved.Folders)
	}
	if saved.Movies.Quality != "2160p" || saved.Movies.MaxSizeGB != 40 || saved.Movies.MinSeeders != 2 || saved.Shows.MaxEpisodeGB != 3 {
		t.Errorf("movie rules: %+v", saved.Movies)
	}
	if saved.Schedule.RSSMinutes != 30 || saved.Schedule.SearchHours != 0 || saved.Jellyfin.URL != "http://localhost:8096" {
		t.Errorf("schedule/jellyfin: %+v %+v", saved.Schedule, saved.Jellyfin)
	}
}

func TestRelativeFolderRefused(t *testing.T) {
	var c Change
	c.QBittorrent.Category = "oneforarr"
	c.Folders = store.Folders{Movies: "movies", Shows: "/data/tv", Downloads: "/data/downloads", QBitRoot: "/data", AppRoot: "/data"}
	c.Movies = store.MovieRules{Quality: "1080p"}
	c.Shows = store.ShowRules{Quality: "1080p"}
	if _, err := apply(store.Defaults().Settings, c); err == nil {
		t.Fatal("relative folder accepted")
	}
}
