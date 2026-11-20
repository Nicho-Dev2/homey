// Package movies is the Radarr part of the app: the movie library, finding
// a good release, sending it to qBittorrent, and putting the finished file
// in the movies folder.
package movies

import (
	"context"
	"errors"
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

// Service is the movies part of the app.
type Service struct {
	Store    *store.Store
	Indexers *indexers.Service
	Now      func() time.Time
	// OnImport, when set, is called after a file is put in the library.
	OnImport func()

	// busy keeps two searches for one movie from running at once.
	busy sync.Map
}

// New returns the movies service.
func New(s *store.Store, idx *indexers.Service) *Service {
	return &Service{Store: s, Indexers: idx, Now: time.Now}
}

func (s *Service) tmdb() *metadata.TMDB {
	return metadata.NewTMDB(s.Store.Read().Settings.Metadata.TMDBKey)
}

var (
	errNotFound = errors.New("That movie isn't in the library.")
	errExists   = errors.New("That movie is already in the library.")
)

// Get returns one movie.
func (s *Service) Get(id int) (store.Movie, bool) {
	for _, m := range s.Store.Read().Movies {
		if m.TMDBID == id {
			return m, true
		}
	}
	return store.Movie{}, false
}

// update changes one movie in the store.
func (s *Service) update(id int, change func(m *store.Movie)) error {
	return s.Store.Update(func(d *store.Data) error {
		for i := range d.Movies {
			if d.Movies[i].TMDBID == id {
				change(&d.Movies[i])
				return nil
			}
		}
		return errNotFound
	})
}

// Add looks the movie up on TMDB and adds it. If its folder already holds
// a video file, that file counts as downloaded.
func (s *Service) Add(ctx context.Context, tmdbID int, quality string) (store.Movie, error) {
	return s.AddFound(ctx, tmdbID, quality, "")
}

// AddFound adds a movie found in the movies folder at path: a folder
// (which becomes the movie's folder) or a video file. Empty path is Add.
func (s *Service) AddFound(ctx context.Context, tmdbID int, quality, path string) (store.Movie, error) {
	if _, ok := s.Get(tmdbID); ok {
		return store.Movie{}, errExists
	}
	info, err := s.tmdb().Movie(ctx, tmdbID)
	if err != nil {
		return store.Movie{}, err
	}
	m := store.Movie{
		TMDBID: info.TMDBID, IMDBID: info.IMDBID, Title: info.Title, OriginalTitle: info.OriginalTitle,
		Year: info.Year, Overview: info.Overview, PosterPath: info.PosterPath, BackdropPath: info.BackdropPath,
		Runtime: info.Runtime, Quality: library.PresetByID(quality).ID, AddedAt: s.Now(),
	}
	set := s.Store.Read().Settings
	switch info, err := os.Stat(path); {
	case path == "":
		m.File = findExisting(filepath.Join(set.Folders.Movies, FolderName(m)), s.Now())
	case err != nil:
		return store.Movie{}, err
	case info.IsDir():
		if filepath.Base(path) != FolderName(m) {
			m.Folder = filepath.Base(path)
		}
		m.File = findExisting(path, s.Now())
	default:
		m.File = findExisting(path, s.Now())
	}
	err = s.Store.Update(func(d *store.Data) error {
		for _, x := range d.Movies {
			if x.TMDBID == m.TMDBID {
				return errExists
			}
		}
		d.Movies = append(d.Movies, m)
		return nil
	})
	return m, err
}

// Change sets a movie's quality preset.
func (s *Service) Change(id int, quality string) (store.Movie, error) {
	err := s.update(id, func(m *store.Movie) { m.Quality = library.PresetByID(quality).ID })
	m, _ := s.Get(id)
	return m, err
}

// Remove takes a movie out of the library. With deleteFiles, its folder in
// the movies folder is deleted too. The torrent in qBittorrent is left
// alone (it may still be seeding).
func (s *Service) Remove(id int, deleteFiles bool) error {
	m, ok := s.Get(id)
	if !ok {
		return errNotFound
	}
	if deleteFiles {
		root := s.Store.Read().Settings.Folders.Movies
		if err := library.RemoveFolder(root, filepath.Join(root, FolderName(m))); err != nil {
			return errors.New("Couldn't delete the movie's folder: " + err.Error())
		}
	}
	return s.Store.Update(func(d *store.Data) error {
		d.Movies = slices.DeleteFunc(d.Movies, func(x store.Movie) bool { return x.TMDBID == id })
		return nil
	})
}

// FolderName is the movie's folder in the library: "Title (Year)", the
// naming Jellyfin expects. Characters not allowed in file names are dropped.
func FolderName(m store.Movie) string {
	if m.Folder != "" {
		return m.Folder
	}
	name := library.CleanFileName(m.Title)
	if m.Year > 0 {
		name += " (" + strconv.Itoa(m.Year) + ")"
	}
	return name
}

// findExisting returns the largest video file in dir, if any.
func findExisting(dir string, now time.Time) *store.MediaFile {
	files := library.VideoFiles(dir)
	if len(files) == 0 {
		return nil
	}
	f := files[0]
	info := releasename.Parse(filepath.Base(f.Path))
	return &store.MediaFile{Path: f.Path, Size: f.Size, Quality: info.Quality(), Resolution: info.Resolution, ImportedAt: now}
}

// CheckFiles brings the saved files in line with the movies folder: a movie
// whose file is gone loses it (it becomes wanted again), and a movie without
// a file gets the video file found in its folder, e.g. one dropped there by
// hand. Movies being downloaded are left alone. Call it only when the movies
// folder can be read, or every file would look gone.
func (s *Service) CheckFiles() (found, missing int) {
	root := s.Store.Read().Settings.Folders.Movies
	now := s.Now()
	_ = s.Store.Update(func(d *store.Data) error {
		for i := range d.Movies {
			m := &d.Movies[i]
			if m.Download != nil {
				continue
			}
			if m.File != nil {
				if _, err := os.Stat(m.File.Path); errors.Is(err, os.ErrNotExist) {
					m.File = nil
					missing++
				}
			}
			if m.File == nil {
				if f := findExisting(filepath.Join(root, FolderName(*m)), now); f != nil {
					m.File = f
					found++
				}
			}
		}
		return nil
	})
	return found, missing
}
