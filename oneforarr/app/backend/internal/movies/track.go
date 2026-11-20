package movies

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/releasename"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/downloads"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
)

// forgetAfter is how long a download may be missing from qBittorrent
// before the app gives up on it (qBittorrent needs a moment to add a
// torrent from a .torrent address).
const forgetAfter = 5 * time.Minute

// Tick does the regular work: follows downloads and imports finished
// ones. The app calls it every 30 seconds.
func (s *Service) Tick(ctx context.Context) {
	if err := s.CheckDownloads(ctx); err != nil {
		log.Printf("movies: checking downloads: %v", err)
	}
}

// CheckDownloads asks qBittorrent how each movie download is doing, and
// imports the ones that finished.
func (s *Service) CheckDownloads(ctx context.Context) error {
	var active []store.Movie
	for _, m := range s.Store.Read().Movies {
		if m.Download != nil {
			active = append(active, m)
		}
	}
	if len(active) == 0 {
		return nil
	}
	set := s.Store.Read().Settings
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	qb, err := downloads.Connect(ctx, set)
	if err != nil {
		return err
	}
	list, err := qb.Torrents(ctx, set.QBittorrent.Category)
	if err != nil {
		return err
	}
	for _, m := range active {
		var t *downloads.Torrent
		for i := range list {
			if list[i].HasTag(m.Download.Tag) {
				t = &list[i]
				break
			}
		}
		switch {
		case t == nil && s.Now().Sub(m.Download.SentAt) > forgetAfter:
			_ = s.update(m.TMDBID, func(x *store.Movie) {
				x.Download = nil
				x.LastSearch = &store.SearchNote{At: s.Now(),
					Message: "The download disappeared from qBittorrent (removed, or the link didn't work)."}
			})
		case t == nil:
			// Not there yet.
		case t.Done():
			s.importMovie(m, *t, set)
		default:
			_ = s.update(m.TMDBID, func(x *store.Movie) {
				if x.Download == nil {
					return
				}
				x.Download.Hash, x.Download.Progress, x.Download.State = t.Hash, t.Progress, t.State
				x.Download.Size, x.Download.ETA, x.Download.Error = t.Size, t.ETA, ""
				if t.State == "error" || t.State == "missingFiles" {
					x.Download.Error = "qBittorrent reports a problem (" + t.State + ")."
				}
				downloads.NoteStuck(x.Download, *t, s.Now())
			})
			if now, _ := s.Get(m.TMDBID); now.Download != nil && downloads.Dead(*now.Download, set.QBittorrent.GiveUpHours, s.Now()) {
				s.giveUp(ctx, qb, now, set.QBittorrent.GiveUpHours)
			}
		}
	}
	return nil
}

// giveUp removes a dead download (no seeders, no progress) from
// qBittorrent, blocks its release and searches for another one.
func (s *Service) giveUp(ctx context.Context, qb *downloads.QBittorrent, m store.Movie, hours int) {
	d := *m.Download
	if err := qb.Delete(ctx, d.Hash); err != nil {
		log.Printf("movies: removing dead download %s: %v", d.ReleaseTitle, err)
		return
	}
	note := fmt.Sprintf("Gave up on %s after %d hours without seeders.", d.ReleaseTitle, hours)
	log.Printf("movies: %s (%s); searching again", note, m.Title)
	_ = s.update(m.TMDBID, func(x *store.Movie) {
		x.Download = nil
		x.Blocked = append(x.Blocked, d.ReleaseTitle)
		x.LastSearch = &store.SearchNote{At: s.Now(), Message: note}
	})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		msg, err := s.SearchAndGrab(ctx, m.TMDBID)
		if err != nil {
			msg = err.Error()
		}
		_ = s.update(m.TMDBID, func(x *store.Movie) {
			x.LastSearch = &store.SearchNote{At: s.Now(), Message: note + " " + msg}
		})
	}()
}

// importMovie puts a finished download's video file into the library and
// clears the download. The torrent keeps seeding from the downloads folder.
func (s *Service) importMovie(m store.Movie, t downloads.Torrent, set store.Settings) {
	file, err := s.place(m, t, set)
	_ = s.update(m.TMDBID, func(x *store.Movie) {
		if err != nil {
			if x.Download != nil {
				x.Download.Progress, x.Download.State = 1, t.State
				x.Download.Error = "Downloaded, but importing failed: " + err.Error()
			}
			return
		}
		x.File = file
		x.Download = nil
		x.LastSearch = &store.SearchNote{At: s.Now(), Message: "Imported " + filepath.Base(file.Path) + "."}
	})
	if err != nil {
		log.Printf("movies: importing %s: %v", m.Title, err)
		return
	}
	log.Printf("movies: imported %s", file.Path)
	if s.OnImport != nil {
		s.OnImport()
	}
}

// place links (or, across drives, copies) the video file into
// <movies>/<Title (Year)>/<Title (Year)> <Quality>.<ext>.
func (s *Service) place(m store.Movie, t downloads.Torrent, set store.Settings) (*store.MediaFile, error) {
	files := library.VideoFiles(set.Folders.ToApp(t.ContentPath))
	if len(files) == 0 {
		return nil, fmt.Errorf("no video file found in %s (check the path mapping in Settings)", set.Folders.ToApp(t.ContentPath))
	}
	src := files[0]
	quality := releasename.Parse(t.Name).Quality()
	if quality == "Unknown" {
		quality = releasename.Parse(filepath.Base(src.Path)).Quality()
	}
	dir := filepath.Join(set.Folders.Movies, FolderName(m))
	name := FolderName(m)
	if quality != "Unknown" {
		name += " " + quality
	}
	dst := filepath.Join(dir, name+strings.ToLower(filepath.Ext(src.Path)))
	if err := placeOver(src.Path, dst); err != nil {
		return nil, err
	}
	// Remove the file it replaces (an upgrade or a replacement), wherever it
	// is in the movies folder: its own folder, or loose (found by a scan).
	if m.File != nil && m.File.Path != dst && insideFolder(m.File.Path, set.Folders.Movies) {
		_ = os.Remove(m.File.Path)
	}
	res := releasename.Parse(t.Name).Resolution
	if res == 0 {
		res = releasename.Parse(filepath.Base(src.Path)).Resolution
	}
	return &store.MediaFile{Path: dst, Size: src.Size, Quality: quality, Resolution: res, ImportedAt: s.Now()}, nil
}

// placeOver puts src at dst like library.Place, but replaces a file already
// at dst (a replacement with the same quality gets the same name). The new
// file is placed next to it first, then renamed over it, so the old one
// stays playable until the new one is complete.
func placeOver(src, dst string) error {
	if _, err := os.Stat(dst); err != nil {
		return library.Place(src, dst)
	}
	tmp := dst + ".oneforarr-new"
	_ = os.Remove(tmp)
	if err := library.Place(src, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// insideFolder says whether path is somewhere under folder.
func insideFolder(path, folder string) bool {
	if folder == "" {
		return false
	}
	rel, err := filepath.Rel(folder, path)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}
