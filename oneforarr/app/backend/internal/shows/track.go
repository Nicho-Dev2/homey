package shows

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/releasename"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/downloads"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
)

// forgetAfter is how long a download may be missing from qBittorrent
// before the app gives up on it.
const forgetAfter = 5 * time.Minute

// refreshEvery is how often each show's episode list is updated.
const refreshEvery = 24 * time.Hour

// Tick does the regular work: follows downloads, imports finished ones,
// and refreshes one show's episode list if it's due. The app calls it every
// 30 seconds.
func (s *Service) Tick(ctx context.Context) {
	if err := s.CheckDownloads(ctx); err != nil {
		log.Printf("shows: checking downloads: %v", err)
	}
	for _, sh := range s.Store.Read().Shows {
		if s.Now().Sub(sh.RefreshedAt) > refreshEvery {
			if err := s.Refresh(ctx, sh.TMDBID); err != nil {
				log.Printf("shows: refreshing %s: %v", sh.Title, err)
			}
			break // one per tick, to spread the work out
		}
	}
}

// CheckDownloads asks qBittorrent how each show download is doing, and
// imports the ones that finished.
func (s *Service) CheckDownloads(ctx context.Context) error {
	active := false
	for _, sh := range s.Store.Read().Shows {
		active = active || len(sh.Downloads) > 0
	}
	if !active {
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
	for _, sh := range s.Store.Read().Shows {
		for _, d := range sh.Downloads {
			s.checkOne(ctx, qb, sh, d, list, set)
		}
	}
	return nil
}

func (s *Service) checkOne(ctx context.Context, qb *downloads.QBittorrent, sh store.Show, d store.ShowDownload, list []downloads.Torrent, set store.Settings) {
	var t *downloads.Torrent
	for i := range list {
		if list[i].HasTag(d.Tag) {
			t = &list[i]
			break
		}
	}
	switch {
	case t == nil && s.Now().Sub(d.SentAt) > forgetAfter:
		_ = s.update(sh.TMDBID, func(x *store.Show) {
			x.Downloads = without(x.Downloads, d.Tag)
			x.LastSearch = &store.SearchNote{At: s.Now(),
				Message: fmt.Sprintf("The download for %s disappeared from qBittorrent (removed, or the link didn't work).", targetOf(d))}
		})
	case t == nil:
		// Not there yet.
	case t.Done():
		s.importDownload(sh, d, *t, set)
	default:
		dead := false
		_ = s.update(sh.TMDBID, func(x *store.Show) {
			for i := range x.Downloads {
				if x.Downloads[i].Tag == d.Tag {
					dl := &x.Downloads[i]
					dl.Hash, dl.Progress, dl.State, dl.Size, dl.ETA, dl.Error = t.Hash, t.Progress, t.State, t.Size, t.ETA, ""
					if t.State == "error" || t.State == "missingFiles" {
						dl.Error = "qBittorrent reports a problem (" + t.State + ")."
					}
					downloads.NoteStuck(&dl.Download, *t, s.Now())
					if downloads.Dead(dl.Download, set.QBittorrent.GiveUpHours, s.Now()) {
						dead, d = true, *dl
					}
				}
			}
		})
		if dead {
			s.giveUp(ctx, qb, sh, d, set.QBittorrent.GiveUpHours)
		}
	}
}

// giveUp removes a dead download (no seeders, no progress) from
// qBittorrent, blocks its release and searches for the same episodes again.
func (s *Service) giveUp(ctx context.Context, qb *downloads.QBittorrent, sh store.Show, d store.ShowDownload, hours int) {
	if err := qb.Delete(ctx, d.Hash); err != nil {
		log.Printf("shows: removing dead download %s: %v", d.ReleaseTitle, err)
		return
	}
	note := fmt.Sprintf("Gave up on %s after %d hours without seeders.", d.ReleaseTitle, hours)
	log.Printf("shows: %s (%s); searching again", note, sh.Title)
	_ = s.update(sh.TMDBID, func(x *store.Show) {
		x.Downloads = without(x.Downloads, d.Tag)
		x.Blocked = append(x.Blocked, d.ReleaseTitle)
		x.LastSearch = &store.SearchNote{At: s.Now(), Message: note}
	})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		msg, err := s.SearchAndGrab(ctx, sh.TMDBID, targetOf(d))
		if err != nil {
			msg = err.Error()
		}
		_ = s.update(sh.TMDBID, func(x *store.Show) {
			x.LastSearch = &store.SearchNote{At: s.Now(), Message: note + " " + msg}
		})
	}()
}

func targetOf(d store.ShowDownload) Target { return Target{Season: d.Season, Episode: d.Episode} }

func without(list []store.ShowDownload, tag string) []store.ShowDownload {
	return slices.DeleteFunc(slices.Clone(list), func(d store.ShowDownload) bool { return d.Tag == tag })
}

// placed is one episode file put in the library.
type placed struct {
	season, episode int
	file            store.MediaFile
}

// importDownload links each episode file of a finished download into the
// show's folder. Files whose name doesn't say which episode they are, or
// that aren't part of what was downloaded, are left alone.
func (s *Service) importDownload(sh store.Show, d store.ShowDownload, t downloads.Torrent, set store.Settings) {
	files := library.VideoFiles(set.Folders.ToApp(t.ContentPath))
	torrentInfo := releasename.Parse(t.Name)
	var done []placed
	var problems []string
	for _, f := range files {
		info := releasename.Parse(filepath.Base(f.Path))
		season, number := info.Season, info.Episode
		if number == 0 && len(files) == 1 {
			// A single-file episode named only by its torrent.
			season, number = torrentInfo.Season, torrentInfo.Episode
		}
		if season == 0 {
			season = d.Season
		}
		if number == 0 || season != d.Season || (d.Episode != 0 && number != d.Episode) {
			continue
		}
		e, ok := findEpisode(sh, season, number)
		if !ok {
			problems = append(problems, fmt.Sprintf("S%02dE%02d isn't in the episode list", season, number))
			continue
		}
		quality := torrentInfo.Quality()
		if quality == "Unknown" {
			quality = info.Quality()
		}
		dst := EpisodePath(set.Folders.Shows, sh, e, quality, strings.ToLower(filepath.Ext(f.Path)))
		if err := library.Place(f.Path, dst); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		res := torrentInfo.Resolution
		if res == 0 {
			res = info.Resolution
		}
		done = append(done, placed{season, number, store.MediaFile{Path: dst, Size: f.Size, Quality: quality, Resolution: res, ImportedAt: s.Now()}})
	}

	_ = s.update(sh.TMDBID, func(x *store.Show) {
		if len(done) == 0 {
			for i := range x.Downloads {
				if x.Downloads[i].Tag == d.Tag {
					x.Downloads[i].Progress, x.Downloads[i].State = 1, t.State
					why := "no episode files found in " + set.Folders.ToApp(t.ContentPath) + " (check the path mapping in Settings)"
					if len(files) > 0 {
						why = "no file matched the episodes downloaded"
					}
					if len(problems) > 0 {
						why = strings.Join(problems, "; ")
					}
					x.Downloads[i].Error = "Downloaded, but importing failed: " + why + "."
				}
			}
			return
		}
		for _, p := range done {
			for i := range x.Episodes {
				e := &x.Episodes[i]
				if e.Season == p.season && e.Number == p.episode {
					if e.File != nil && e.File.Path != p.file.Path && filepath.Dir(e.File.Path) == filepath.Dir(p.file.Path) {
						removeOld(e.File.Path)
					}
					f := p.file
					e.File = &f
				}
			}
		}
		x.Downloads = without(x.Downloads, d.Tag)
		msg := "Imported " + plural(len(done), "episode") + " of " + targetOf(d).String() + "."
		if len(problems) > 0 {
			msg += " Skipped: " + strings.Join(problems, "; ") + "."
		}
		x.LastSearch = &store.SearchNote{At: s.Now(), Message: msg}
	})
	if len(done) == 0 {
		log.Printf("shows: importing %s %s: nothing imported (%s)", sh.Title, targetOf(d), strings.Join(problems, "; "))
		return
	}
	log.Printf("shows: imported %s for %s %s", plural(len(done), "episode"), sh.Title, targetOf(d))
	if s.OnImport != nil {
		s.OnImport()
	}
}

func findEpisode(sh store.Show, season, number int) (store.Episode, bool) {
	for _, e := range sh.Episodes {
		if e.Season == season && e.Number == number {
			return e, true
		}
	}
	return store.Episode{}, false
}

// removeOld deletes an episode's previous file when a new one replaces it.
func removeOld(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("shows: removing old file %s: %v", path, err)
	}
}
