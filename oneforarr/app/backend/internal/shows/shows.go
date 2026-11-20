// Package shows is the Sonarr part of the app: TV shows and their episodes,
// finding releases (single episodes or whole seasons), sending them to
// qBittorrent, and putting the finished files in the TV folder.
package shows

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/releasename"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/indexers"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/metadata"
)

// Monitor choices: which episodes to follow.
const (
	MonitorAll    = "all"    // every episode
	MonitorFuture = "future" // only episodes that haven't aired yet
	MonitorLatest = "latest" // the newest season, and seasons after it
	MonitorNone   = "none"   // nothing, until chosen by hand
)

// Service is the shows part of the app.
type Service struct {
	Store    *store.Store
	Indexers *indexers.Service
	TVmaze   *metadata.TVmaze
	Now      func() time.Time
	// OnImport, when set, is called after files are put in the library.
	OnImport func()

	busy   sync.Map   // "id:S01E02", "id:season 1" or "id:all" → a search is running
	grabMu sync.Mutex // one grab at a time (see Grab)
}

// New returns the shows service.
func New(s *store.Store, idx *indexers.Service) *Service {
	return &Service{Store: s, Indexers: idx, TVmaze: metadata.NewTVmaze(), Now: time.Now}
}

func (s *Service) tmdb() *metadata.TMDB {
	return metadata.NewTMDB(s.Store.Read().Settings.Metadata.TMDBKey)
}

var (
	errNotFound = errors.New("That show isn't in the library.")
	errExists   = errors.New("That show is already in the library.")
)

// Get returns one show.
func (s *Service) Get(id int) (store.Show, bool) {
	for _, sh := range s.Store.Read().Shows {
		if sh.TMDBID == id {
			return sh, true
		}
	}
	return store.Show{}, false
}

func (s *Service) update(id int, change func(sh *store.Show)) error {
	return s.Store.Update(func(d *store.Data) error {
		for i := range d.Shows {
			if d.Shows[i].TMDBID == id {
				change(&d.Shows[i])
				return nil
			}
		}
		return errNotFound
	})
}

// Add looks the show up on TMDB, gets its episodes from TVmaze, marks which
// to follow, and finds episodes already in its folder.
func (s *Service) Add(ctx context.Context, tmdbID int, quality, monitor string) (store.Show, error) {
	return s.AddFound(ctx, tmdbID, quality, monitor, "")
}

// AddFound adds a show whose folder in the TV folder is named folder (found
// by a scan). Empty folder is Add.
func (s *Service) AddFound(ctx context.Context, tmdbID int, quality, monitor, folder string) (store.Show, error) {
	if _, ok := s.Get(tmdbID); ok {
		return store.Show{}, errExists
	}
	info, err := s.tmdb().Show(ctx, tmdbID)
	if err != nil {
		return store.Show{}, err
	}
	if !validMonitor(monitor) {
		monitor = MonitorAll
	}
	sh := store.Show{
		TMDBID: info.TMDBID, TVDBID: info.TVDBID, IMDBID: info.IMDBID, Title: info.Title,
		OriginalTitle: info.OriginalName, Year: info.Year, Overview: info.Overview,
		PosterPath: info.PosterPath, BackdropPath: info.BackdropPath, Status: info.Status,
		Quality: library.PresetByID(quality).ID, Monitor: monitor, AddedAt: s.Now(),
		Episodes: []store.Episode{}, Downloads: []store.ShowDownload{},
	}
	sh.TVmazeID, err = s.TVmaze.FindShow(ctx, sh.TVDBID, sh.IMDBID)
	if err != nil {
		return store.Show{}, err
	}
	eps, err := s.TVmaze.Episodes(ctx, sh.TVmazeID)
	if err != nil {
		return store.Show{}, err
	}
	if folder != "" && folder != FolderName(sh) {
		sh.Folder = folder
	}
	sh.Episodes = merge(sh, nil, eps, s.today())
	sh.RefreshedAt = s.Now()
	s.findExisting(&sh)

	err = s.Store.Update(func(d *store.Data) error {
		for _, x := range d.Shows {
			if x.TMDBID == sh.TMDBID {
				return errExists
			}
		}
		d.Shows = append(d.Shows, sh)
		return nil
	})
	return sh, err
}

func validMonitor(m string) bool {
	return m == MonitorAll || m == MonitorFuture || m == MonitorLatest || m == MonitorNone
}

// today is the date as YYYY-MM-DD, to compare with air dates.
func (s *Service) today() string { return s.Now().Format("2006-01-02") }

// Refresh gets the episode list again from TVmaze: new episodes appear
// (followed according to the show's Monitor choice), changed titles and
// dates update, and choices and files are kept.
func (s *Service) Refresh(ctx context.Context, id int) error {
	sh, ok := s.Get(id)
	if !ok {
		return errNotFound
	}
	tvmazeID := sh.TVmazeID
	var eps []metadata.Episode
	var err error
	if tvmazeID == 0 {
		tvmazeID, err = s.TVmaze.FindShow(ctx, sh.TVDBID, sh.IMDBID)
	}
	if err == nil {
		eps, err = s.TVmaze.Episodes(ctx, tvmazeID)
	}
	// Read what's needed before s.update: its change function runs while
	// the data is locked, so it must not read the store itself (that waits
	// forever for the lock — the app froze on its first daily refresh).
	root, now, today := s.Store.Read().Settings.Folders.Shows, s.Now(), s.today()
	return s.update(id, func(x *store.Show) {
		x.RefreshedAt = now
		if err != nil {
			x.RefreshError = err.Error()
			return
		}
		x.RefreshError = ""
		x.TVmazeID = tvmazeID
		x.Episodes = merge(*x, x.Episodes, eps, today)
		attachFiles(x, filepath.Join(root, FolderName(*x)), now)
	})
}

// merge builds the new episode list: TVmaze's episodes, keeping what the
// app knows about each (followed or not, file). Episodes TVmaze no longer
// lists are dropped unless they have a file.
func merge(sh store.Show, old []store.Episode, fresh []metadata.Episode, today string) []store.Episode {
	known := map[[2]int]store.Episode{}
	for _, e := range old {
		known[[2]int{e.Season, e.Number}] = e
	}
	// For "latest": the newest season known before this update (or, when
	// adding, the newest season there is). New episodes in it or in later
	// seasons are followed.
	latest := 0
	for _, e := range old {
		latest = max(latest, e.Season)
	}
	if len(old) == 0 {
		for _, e := range fresh {
			latest = max(latest, e.Season)
		}
	}
	out := make([]store.Episode, 0, len(fresh))
	seen := map[[2]int]bool{}
	for _, f := range fresh {
		key := [2]int{f.Season, f.Number}
		seen[key] = true
		e, had := known[key]
		if !had {
			e.Monitored = followNew(sh.Monitor, f, latest, today)
		}
		e.Season, e.Number, e.Title, e.AirDate, e.AirStamp = f.Season, f.Number, f.Title, f.AirDate, f.AirStamp
		e.Runtime, e.Overview = f.Runtime, f.Overview
		out = append(out, e)
	}
	for _, e := range old {
		if !seen[[2]int{e.Season, e.Number}] && e.File != nil {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b store.Episode) int {
		if a.Season != b.Season {
			return a.Season - b.Season
		}
		return a.Number - b.Number
	})
	return out
}

// followNew decides whether a newly listed episode is followed.
func followNew(monitor string, e metadata.Episode, latest int, today string) bool {
	switch monitor {
	case MonitorAll:
		return true
	case MonitorFuture:
		return e.AirDate == "" || e.AirDate > today
	case MonitorLatest:
		return e.Season >= latest
	}
	return false
}

// Aired reports whether an episode has aired by today.
func Aired(e store.Episode, now time.Time) bool {
	if !e.AirStamp.IsZero() {
		return !e.AirStamp.After(now)
	}
	return e.AirDate != "" && e.AirDate <= now.Format("2006-01-02")
}

// FolderName is the show's folder: "Title (Year)".
func FolderName(sh store.Show) string {
	if sh.Folder != "" {
		return sh.Folder
	}
	name := library.CleanFileName(sh.Title)
	if sh.Year > 0 {
		name += " (" + strconv.Itoa(sh.Year) + ")"
	}
	return name
}

// EpisodePath is where an episode's file goes:
// <tv>/<Show (Year)>/Season 01/<Show (Year)> - S01E02 - <Title> <Quality>.ext
func EpisodePath(root string, sh store.Show, e store.Episode, quality, ext string) string {
	folder := FolderName(sh)
	name := fmt.Sprintf("%s - S%02dE%02d", folder, e.Season, e.Number)
	if t := library.CleanFileName(e.Title); t != "" {
		name += " - " + t
	}
	if quality != "" && quality != "Unknown" {
		name += " " + quality
	}
	return filepath.Join(root, folder, fmt.Sprintf("Season %02d", e.Season), name+ext)
}

// findExisting attaches video files already in the show's folder to their
// episodes, reading SxxEyy from each file name.
func (s *Service) findExisting(sh *store.Show) {
	attachFiles(sh, filepath.Join(s.Store.Read().Settings.Folders.Shows, FolderName(*sh)), s.Now())
}

// attachFiles gives episodes without a file the matching video file in dir
// and returns how many it attached.
func attachFiles(sh *store.Show, dir string, now time.Time) int {
	n := 0
	for _, f := range library.VideoFiles(dir) {
		info := releasename.Parse(filepath.Base(f.Path))
		if info.Season == 0 || info.Episode == 0 {
			continue
		}
		for i := range sh.Episodes {
			e := &sh.Episodes[i]
			if e.Season == info.Season && e.Number == info.Episode && e.File == nil {
				e.File = &store.MediaFile{Path: f.Path, Size: f.Size, Quality: info.Quality(), Resolution: info.Resolution, ImportedAt: now}
				n++
			}
		}
	}
	return n
}

// Change sets a show's quality preset.
func (s *Service) Change(id int, quality string) (store.Show, error) {
	err := s.update(id, func(sh *store.Show) { sh.Quality = library.PresetByID(quality).ID })
	sh, _ := s.Get(id)
	return sh, err
}

// SetMonitored follows or unfollows one episode, or a whole season
// (episode 0).
func (s *Service) SetMonitored(id, season, episode int, on bool) (store.Show, error) {
	err := s.update(id, func(sh *store.Show) {
		for i := range sh.Episodes {
			e := &sh.Episodes[i]
			if e.Season == season && (episode == 0 || e.Number == episode) {
				e.Monitored = on
			}
		}
	})
	sh, _ := s.Get(id)
	return sh, err
}

// Remove takes a show out of the library; with deleteFiles, its folder in
// the TV folder too. Torrents in qBittorrent are left to seed.
func (s *Service) Remove(id int, deleteFiles bool) error {
	sh, ok := s.Get(id)
	if !ok {
		return errNotFound
	}
	if deleteFiles {
		root := s.Store.Read().Settings.Folders.Shows
		if err := library.RemoveFolder(root, filepath.Join(root, FolderName(sh))); err != nil {
			return errors.New("Couldn't delete the show's folder: " + err.Error())
		}
	}
	return s.Store.Update(func(d *store.Data) error {
		d.Shows = slices.DeleteFunc(d.Shows, func(x store.Show) bool { return x.TMDBID == id })
		return nil
	})
}

// CheckFiles brings the saved episode files in line with the TV folder:
// episodes whose file is gone lose it, and video files in a show's folder
// are attached to their episodes (SxxEyy in the name). Call it only when
// the TV folder can be read.
func (s *Service) CheckFiles() (found, missing int) {
	now := s.Now()
	_ = s.Store.Update(func(d *store.Data) error {
		for i := range d.Shows {
			sh := &d.Shows[i]
			for j := range sh.Episodes {
				if e := &sh.Episodes[j]; e.File != nil {
					if _, err := os.Stat(e.File.Path); errors.Is(err, os.ErrNotExist) {
						e.File = nil
						missing++
					}
				}
			}
			found += attachFiles(sh, filepath.Join(d.Settings.Folders.Shows, FolderName(*sh)), now)
		}
		return nil
	})
	return found, missing
}
