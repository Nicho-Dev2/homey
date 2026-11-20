package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewFileGetsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "data.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Read().Settings.QBittorrent.URL; got != "http://gluetun:8080" {
		t.Errorf("default qBittorrent URL = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600 (it holds passwords)", info.Mode().Perm())
	}
}

func TestUpdateSavesAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	s, _ := Open(path)
	err := s.Update(func(d *Data) error {
		d.Settings.Metadata.TMDBKey = "abc"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Read().Settings.Metadata.TMDBKey != "abc" {
		t.Error("change not saved")
	}
}

func TestFailedUpdateChangesNothing(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "data.json"))
	_ = s.Update(func(d *Data) error {
		d.Settings.Metadata.TMDBKey = "half done"
		return errors.New("stop")
	})
	if s.Read().Settings.Metadata.TMDBKey != "" {
		t.Error("failed update was kept")
	}
}

func TestReadIsACopy(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "data.json"))
	d := s.Read()
	d.Settings.Folders.Movies = "/elsewhere"
	d.Sessions["x"] = Session{}
	if s.Read().Settings.Folders.Movies != "/data/movies" || len(s.Read().Sessions) != 0 {
		t.Error("changing a Read copy changed the store")
	}
}

func TestPathMapping(t *testing.T) {
	f := Folders{QBitRoot: "/data", AppRoot: "/Volumes/MyDrive/Media/"}
	cases := map[string]string{
		"/data/downloads/x.mkv": "/Volumes/MyDrive/Media/downloads/x.mkv",
		"/data":                 "/Volumes/MyDrive/Media",
		"/database/x":           "/database/x", // only whole folder names
		"/other/x":              "/other/x",
	}
	for in, want := range cases {
		if got := f.ToApp(in); got != want {
			t.Errorf("ToApp(%q) = %q, want %q", in, got, want)
		}
	}
	if got := f.ToQBit("/Volumes/MyDrive/Media/downloads"); got != "/data/downloads" {
		t.Errorf("ToQBit = %q", got)
	}
	same := Folders{QBitRoot: "/data", AppRoot: "/data"}
	if same.ToApp("/data/a") != "/data/a" {
		t.Error("same roots changed the path")
	}
}

func TestOldFileGetsNewDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json")
	os.WriteFile(path, []byte(`{"sessions":{},"settings":{"folders":{"movies":"/m"}}}`), 0o600)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d := s.Read()
	if d.Settings.Movies.Quality != "1080p" || d.Settings.Folders.QBitRoot != "/data" || d.Settings.Folders.Movies != "/m" ||
		!d.Settings.Movies.Upgrades || d.Settings.Schedule.RSSMinutes != 60 || d.Version != 4 || d.Settings.QBittorrent.GiveUpHours != 6 || d.Settings.Folders.MinFreeGB != 50 || !d.Settings.QBittorrent.RestartWhenStuck || d.Settings.Metadata.Region != "US" {
		t.Errorf("defaults: %+v", d.Settings)
	}
}

func TestRepath(t *testing.T) {
	var d Data
	d.Settings.Folders = Folders{Movies: "/Volumes/M/Media/movies", Shows: "/Volumes/M/Media/tv", Downloads: "/Volumes/M/Media/downloads", QBitRoot: "/data", AppRoot: "/Volumes/M/Media"}
	d.Movies = []Movie{{File: &MediaFile{Path: "/Volumes/M/Media/movies/A (2000)/A (2000) Bluray-1080p.mkv"}}, {}}
	d.Shows = []Show{{Episodes: []Episode{{File: &MediaFile{Path: "/Volumes/M/Media/tv/B/Season 01/x.mkv"}}, {}}}}
	d.Settings.Jellyfin.URL = "http://localhost:8096"

	if n := d.Repath("/Volumes/M/Media", "/data"); n != 6 {
		t.Errorf("changed %d, want 6", n)
	}
	f := d.Settings.Folders
	if f.Movies != "/data/movies" || f.Shows != "/data/tv" || f.Downloads != "/data/downloads" || f.AppRoot != "/data" || f.QBitRoot != "/data" {
		t.Errorf("folders: %+v", f)
	}
	if d.Movies[0].File.Path != "/data/movies/A (2000)/A (2000) Bluray-1080p.mkv" || d.Shows[0].Episodes[0].File.Path != "/data/tv/B/Season 01/x.mkv" {
		t.Errorf("files: %s %s", d.Movies[0].File.Path, d.Shows[0].Episodes[0].File.Path)
	}
	if n := d.Repath("/Volumes/M/Media", "/data"); n != 0 {
		t.Errorf("second run changed %d", n)
	}
}

// A change function that reads the store must not freeze the app (it did:
// the first daily show refresh read the store while it was locked).
func TestReadInsideUpdate(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_ = s.Update(func(d *Data) error {
			d.Settings.QBittorrent.Category = s.Read().Settings.QBittorrent.Category + "x"
			return nil
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Read inside Update froze")
	}
	if s.Read().Settings.QBittorrent.Category != "oneforarrx" {
		t.Errorf("got %q", s.Read().Settings.QBittorrent.Category)
	}
}
