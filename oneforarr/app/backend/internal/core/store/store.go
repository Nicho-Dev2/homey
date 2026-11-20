// Package store keeps all of the app's data in one JSON file.
//
// Every change goes through Update, which writes a new copy of the file and
// then swaps it in, so a crash or power cut never leaves a half-written file.
// The whole file is kept in memory; for a home library (thousands of titles)
// this is small and fast. If it ever outgrows that, only this package changes.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// Data is everything the app saves.
type Data struct {
	// Version is the data file's layout version; see fillNewDefaults.
	Version  int                `json:"version"`
	Owner    *Owner             `json:"owner,omitempty"`
	Sessions map[string]Session `json:"sessions"`
	Settings Settings           `json:"settings"`
	Indexers []Indexer          `json:"indexers"`
	Movies   []Movie            `json:"movies"`
	Shows    []Show             `json:"shows"`
	Library  Library            `json:"library"`
}

// Library is what the last folder scan found that it couldn't add on its
// own (see package scan).
type Library struct {
	LastScan  time.Time   `json:"lastScan,omitzero"`
	Report    string      `json:"report,omitempty"` // one line about the last scan
	Unmatched []Unmatched `json:"unmatched"`
	Skipped   []string    `json:"skipped"` // paths the owner chose to leave alone
}

// Unmatched is a folder or file in the library that isn't a known movie or
// show and couldn't be matched for sure.
type Unmatched struct {
	Kind  string `json:"kind"` // "movie" or "show"
	Path  string `json:"path"`
	Name  string `json:"name"`  // the folder or file name
	Title string `json:"title"` // the title read from the name
	Year  int    `json:"year,omitempty"`
	Note  string `json:"note"` // why it wasn't added
}

// Owner is the one account that can sign in.
type Owner struct {
	Username     string    `json:"username"`
	PasswordHash string    `json:"passwordHash"`
	CreatedAt    time.Time `json:"createdAt"`
}

// Session is one signed-in browser. The map key is a hash of the cookie
// value, so the file never holds a usable session cookie.
type Session struct {
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Indexer is one site or feed the app searches.
type Indexer struct {
	ID string `json:"id"`
	// Kind is "definition" (a site, read with its definition file) or
	// "torznab" (a Torznab/Newznab feed).
	Kind       string `json:"kind"`
	Definition string `json:"definition,omitempty"` // definition id, e.g. "yts"
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	// URL is the site address (definition) or the feed address (torznab).
	URL    string `json:"url"`
	APIKey string `json:"apiKey,omitempty"` // torznab only
	// Settings are the site's options from its definition file: true/false
	// for checkboxes, text for everything else.
	Settings map[string]any `json:"settings,omitempty"`
}

// Movie is one movie in the library, wanted or downloaded.
type Movie struct {
	TMDBID        int       `json:"tmdbId"`
	IMDBID        string    `json:"imdbId,omitempty"`
	Title         string    `json:"title"`
	OriginalTitle string    `json:"originalTitle,omitempty"`
	Year          int       `json:"year"`
	Overview      string    `json:"overview,omitempty"`
	PosterPath    string    `json:"posterPath,omitempty"`
	BackdropPath  string    `json:"backdropPath,omitempty"`
	Runtime       int       `json:"runtime,omitempty"`
	Quality       string    `json:"quality"` // a quality preset, e.g. "1080p"
	AddedAt       time.Time `json:"addedAt"`
	// Folder is the movie's folder name in the movies folder when it isn't
	// the usual "Title (Year)" (found by a scan). Empty means the usual.
	Folder string `json:"folder,omitempty"`
	// File is the movie's video file once it's in the library.
	File *MediaFile `json:"file,omitempty"`
	// Download is the release being downloaded now, if any.
	Download *Download `json:"download,omitempty"`
	// LastSearch says when the app last looked for a release, and what
	// came of it.
	LastSearch *SearchNote `json:"lastSearch,omitempty"`
	// Blocked: release names given up on (no seeders); never picked again.
	Blocked []string `json:"blocked,omitempty"`
	// AutoSearchedAt is when the scheduled search last looked for it.
	AutoSearchedAt time.Time `json:"autoSearchedAt,omitzero"`
}

// MediaFile is a video file in the library: a movie or an episode.
type MediaFile struct {
	Path       string    `json:"path"` // as this app sees it
	Size       int64     `json:"size"`
	Quality    string    `json:"quality"`              // e.g. "Bluray-1080p"
	Resolution int       `json:"resolution,omitempty"` // 1080; 0 unknown
	ImportedAt time.Time `json:"importedAt"`
}

// Download is a release handed to qBittorrent.
type Download struct {
	// Replace: picked by hand to replace the file already in the library
	// (movies), not to fill a gap or upgrade.
	Replace bool `json:"replace,omitempty"`
	// Tag is the qBittorrent tag that marks this download.
	Tag          string `json:"tag"`
	ReleaseTitle string `json:"releaseTitle"`
	IndexerName  string `json:"indexerName"`
	// Link is the kind of link sent: "torrent file", "magnet" or "magnet
	// without trackers" (slow to start; see downloads.LinkKind).
	Link     string    `json:"link,omitempty"`
	SentAt   time.Time `json:"sentAt"`
	Hash     string    `json:"hash,omitempty"`
	Size     int64     `json:"size,omitempty"`
	Progress float64   `json:"progress"` // 0..1
	State    string    `json:"state,omitempty"`
	ETA      int64     `json:"eta,omitempty"` // seconds
	Error    string    `json:"error,omitempty"`
	// StuckSince: since when it has had no seeders and made no progress
	// (zero while it's moving; see downloads.NoteStuck). Downloaded is the
	// bytes seen at the last check.
	StuckSince time.Time `json:"stuckSince,omitzero"`
	Downloaded int64     `json:"downloaded,omitempty"`
}

// Show is one TV show in the library.
type Show struct {
	TMDBID int `json:"tmdbId"`
	// Folder: the show's folder name when it isn't "Title (Year)" (found by
	// a scan). Empty means the usual.
	Folder        string `json:"folder,omitempty"`
	TVDBID        int    `json:"tvdbId,omitempty"`
	TVmazeID      int    `json:"tvmazeId,omitempty"`
	IMDBID        string `json:"imdbId,omitempty"`
	Title         string `json:"title"`
	OriginalTitle string `json:"originalTitle,omitempty"`
	Year          int    `json:"year"`
	Overview      string `json:"overview,omitempty"`
	PosterPath    string `json:"posterPath,omitempty"`
	BackdropPath  string `json:"backdropPath,omitempty"`
	Status        string `json:"status,omitempty"` // TMDB: Returning Series, Ended...
	Quality       string `json:"quality"`
	// Monitor says which new episodes to follow: all, future, latest or none.
	Monitor     string    `json:"monitor"`
	AddedAt     time.Time `json:"addedAt"`
	RefreshedAt time.Time `json:"refreshedAt"`
	// RefreshError is set when the episode list couldn't be updated.
	RefreshError string         `json:"refreshError,omitempty"`
	Episodes     []Episode      `json:"episodes"`
	Downloads    []ShowDownload `json:"downloads"`
	LastSearch   *SearchNote    `json:"lastSearch,omitempty"`
	// Blocked: release names given up on (no seeders); never picked again.
	Blocked []string `json:"blocked,omitempty"`
	// AutoSearchedAt is when the scheduled search last looked for it.
	AutoSearchedAt time.Time `json:"autoSearchedAt,omitzero"`
}

// Episode is one episode of a show.
type Episode struct {
	Season   int       `json:"season"`
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	AirDate  string    `json:"airDate,omitempty"` // YYYY-MM-DD
	AirStamp time.Time `json:"airStamp,omitzero"`
	Runtime  int       `json:"runtime,omitempty"`
	Overview string    `json:"overview,omitempty"`
	// Monitored episodes are searched for when missing.
	Monitored bool       `json:"monitored"`
	File      *MediaFile `json:"file,omitempty"`
}

// ShowDownload is a release sent to qBittorrent for a show: one episode, or
// a whole season (Episode 0).
type ShowDownload struct {
	Download
	Season  int `json:"season"`
	Episode int `json:"episode,omitempty"`
}

// SearchNote is the result of the last search for a movie or show.
type SearchNote struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

// Settings are the choices made on the Settings page.
type Settings struct {
	QBittorrent QBittorrent `json:"qbittorrent"`
	Metadata    Metadata    `json:"metadata"`
	Folders     Folders     `json:"folders"`
	Movies      MovieRules  `json:"movies"`
	Shows       ShowRules   `json:"shows"`
	Schedule    Schedule    `json:"schedule"`
	Jellyfin    Jellyfin    `json:"jellyfin"`
}

// Schedule is the automatic work.
type Schedule struct {
	// RSSMinutes: how often to check the indexers' newest releases (0 = off).
	RSSMinutes int `json:"rssMinutes"`
	// SearchHours: how often to search for each wanted movie and show
	// (0 = off).
	SearchHours int `json:"searchHours"`
}

// Jellyfin is the media server to tell about new files. Empty URL = off.
type Jellyfin struct {
	URL    string `json:"url"`
	APIKey string `json:"apiKey"`
}

// ShowRules decide which releases are good enough for a show.
type ShowRules struct {
	Quality string `json:"quality"`
	// MaxEpisodeGB is the largest download per episode, in GB (0 = no
	// limit). A season pack may be this times its number of episodes.
	MaxEpisodeGB float64 `json:"maxEpisodeGB"`
	MinSeeders   int     `json:"minSeeders"`
	Upgrades     bool    `json:"upgrades"`
}

// MovieRules decide which releases are good enough for a movie.
type MovieRules struct {
	// Quality is the preset for newly added movies.
	Quality string `json:"quality"`
	// MaxSizeGB is the largest release to download, in GB (0 = no limit).
	MaxSizeGB float64 `json:"maxSizeGB"`
	// MinSeeders is the fewest seeders a release may have.
	MinSeeders int `json:"minSeeders"`
	// Upgrades: keep looking until a file reaches the preset's preferred
	// resolution.
	Upgrades bool `json:"upgrades"`
}

// QBittorrent is how to reach the download client.
type QBittorrent struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
	// Category is the qBittorrent category given to our downloads, so we
	// only ever touch torrents we added.
	Category string `json:"category"`
	// RestartWhenStuck: restart qBittorrent when it has had no connection
	// for a while (see package watchdog).
	RestartWhenStuck bool `json:"restartWhenStuck"`
	// GiveUpHours: a download with no seeders and no progress for this
	// long is removed, its release is never picked again, and the app
	// searches for another. 0 = never.
	GiveUpHours int `json:"giveUpHours"`
}

// Metadata holds the key for TMDB (movie and show details, posters).
// TVmaze, used for episodes, needs no key.
type Metadata struct {
	TMDBKey string `json:"tmdbKey"`
	// Region is the two-letter country whose streaming services the
	// Discover page shows, e.g. "US".
	Region string `json:"region"`
}

// Folders are paths as seen inside the container.
type Folders struct {
	Movies    string `json:"movies"`
	Shows     string `json:"shows"`
	Downloads string `json:"downloads"`
	// Path mapping: the same drive as qBittorrent sees it, and as this app
	// sees it. Both are /data when both run in Docker with the same mount.
	QBitRoot string `json:"qbitRoot"`
	AppRoot  string `json:"appRoot"`
	// MinFreeGB: nothing is sent to qBittorrent when it would leave less
	// than this much free on the media drive. 0 = no limit.
	MinFreeGB int `json:"minFreeGB"`
}

// Defaults are used for a new install; they match the arr-stack layout
// (one /data mount holding downloads, movies and tv).
func Defaults() Data {
	return Data{
		Version:  currentVersion,
		Sessions: map[string]Session{},
		Settings: Settings{
			QBittorrent: QBittorrent{URL: "http://gluetun:8080", Category: "oneforarr", RestartWhenStuck: true, GiveUpHours: 6},
			Folders: Folders{Movies: "/data/movies", Shows: "/data/tv", Downloads: "/data/downloads",
				QBitRoot: "/data", AppRoot: "/data", MinFreeGB: 50},
			Metadata: Metadata{Region: "US"},
			Movies:   MovieRules{Quality: "1080p", MaxSizeGB: 20, MinSeeders: 1, Upgrades: true},
			Shows:    ShowRules{Quality: "1080p", MaxEpisodeGB: 4, MinSeeders: 1, Upgrades: true},
			Schedule: Schedule{RSSMinutes: 60, SearchHours: 12},
		},
	}
}

// Store guards the data and its file.
//
// Reading never waits: Read copies the latest saved data. Update takes a
// lock so changes happen one at a time. A change function may call Read
// (it sees the data from before the change); it must not call Update.
type Store struct {
	path string
	mu   sync.Mutex           // one Update at a time
	cur  atomic.Pointer[Data] // the latest saved data
	data Data                 // used only while Open reads the file
}

// Open reads the file at path, or starts with Defaults if it doesn't exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path, data: Defaults()}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create config folder: %w", err)
		}
		s.cur.Store(&s.data)
		return s, s.write(s.data)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, &s.data); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if s.data.Sessions == nil {
		s.data.Sessions = map[string]Session{}
	}
	fillNewDefaults(&s.data)
	s.cur.Store(&s.data)
	return s, nil
}

// Read returns a copy of the data. Changing the copy changes nothing.
func (s *Store) Read() Data {
	return clone(*s.cur.Load())
}

// Update runs change on a copy of the data and saves the result. If change
// returns an error, nothing is saved.
func (s *Store) Update(change func(d *Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := clone(*s.cur.Load())
	if err := change(&next); err != nil {
		return err
	}
	if err := s.write(next); err != nil {
		return err
	}
	s.cur.Store(&next)
	return nil
}

// write saves d to a temporary file, flushes it to disk, then renames it
// over the real file.
func (s *Store) write(d Data) error {
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("save data: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		return fmt.Errorf("save data: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("save data: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("save data: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("save data: %w", err)
	}
	return nil
}

// clone makes a deep copy through JSON. Simple and always correct for
// plain data like this.
func clone(d Data) Data {
	raw, _ := json.Marshal(d)
	var out Data
	_ = json.Unmarshal(raw, &out)
	if out.Sessions == nil {
		out.Sessions = map[string]Session{}
	}
	return out
}

// currentVersion is the data layout version this app writes.
const currentVersion = 4

// fillNewDefaults sets settings added after a data file was first written.
func fillNewDefaults(d *Data) {
	def := Defaults().Settings
	if d.Version < 2 {
		// Version 2 added upgrades and the schedule.
		d.Settings.Movies.Upgrades = true
		d.Settings.Shows.Upgrades = true
		d.Settings.Schedule = def.Schedule
		d.Version = 2
	}
	if d.Version < 3 {
		// Version 3 added the free-space limit, the qBittorrent watchdog and
		// the Discover page's country.
		d.Settings.Folders.MinFreeGB = def.Folders.MinFreeGB
		d.Settings.QBittorrent.RestartWhenStuck = true
		d.Settings.Metadata.Region = def.Metadata.Region
		d.Version = 3
	}
	if d.Version < 4 {
		// Version 4 gives up on dead downloads (the owner chose 6 hours).
		d.Settings.QBittorrent.GiveUpHours = def.QBittorrent.GiveUpHours
		d.Version = 4
	}
	if d.Settings.Movies.Quality == "" {
		d.Settings.Movies = def.Movies
	}
	if d.Settings.Shows.Quality == "" {
		d.Settings.Shows = def.Shows
	}
	if d.Settings.Folders.QBitRoot == "" {
		d.Settings.Folders.QBitRoot = def.Folders.QBitRoot
	}
	if d.Settings.Folders.AppRoot == "" {
		d.Settings.Folders.AppRoot = def.Folders.AppRoot
	}
}
