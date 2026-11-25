package shows

import (
	"context"
	"log"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/releasename"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
)

// Fixed releases. When a group finds a problem in a release (bad sound,
// missing frames), it puts out a REPACK; another group's fix is a PROPER.
// The newest releases (RSS) are checked for fixes of the releases the
// library's episodes came from, and a fix replaces the file it fixes.

// grabFixes sends the fixes among releases for a show's episodes. It
// returns how many were sent.
func (s *Service) grabFixes(ctx context.Context, sh store.Show, releases []model.Release) int {
	rules := s.Store.Read().Settings.Shows
	sent := 0
	done := map[Target]bool{}
	for _, r := range releases {
		info := releasename.Parse(r.Title)
		if info.Fix == 0 || info.Season == 0 || !showTitleMatches(info.Title, sh) {
			continue
		}
		t := Target{Season: info.Season, Episode: info.Episode}
		if done[t] || !fixesAFile(sh, t, r.Title) {
			continue
		}
		// The usual checks (size, seeders, quality), as if the episodes
		// had no file yet.
		current, _ := s.Get(sh.TMDBID)
		if d := judge(withoutFiles(current, t), t, max(len(episodesIn(current, t.Season)), 1), rules, library.PresetByID(current.Quality), r); !d.Accepted {
			continue
		}
		if err := s.Grab(ctx, sh.TMDBID, t, r); err != nil {
			continue // already downloading, or qBittorrent refused
		}
		done[t] = true
		sent++
		log.Printf("shows: fixed release for %s %s: %s", sh.Title, t, r.Title)
		_ = s.update(sh.TMDBID, func(x *store.Show) {
			x.LastSearch = &store.SearchNote{At: s.Now(), Message: "Found a fixed release (" + fixWord(info) + "): sent " + r.Title + " to qBittorrent."}
		})
	}
	return sent
}

// fixesAFile says whether a release fixes the release of a file in its
// target: the episode, or any episode of the season for a season pack.
func fixesAFile(sh store.Show, t Target, title string) bool {
	for _, e := range sh.Episodes {
		if e.Season != t.Season || (t.Episode != 0 && e.Number != t.Episode) {
			continue
		}
		if e.File != nil && library.FixesRelease(title, e.File.Release) {
			return true
		}
	}
	return false
}

// withoutFiles is a copy of the show with no files in the target.
func withoutFiles(sh store.Show, t Target) store.Show {
	sh.Episodes = append([]store.Episode(nil), sh.Episodes...)
	for i, e := range sh.Episodes {
		if e.Season == t.Season && (t.Episode == 0 || e.Number == t.Episode) {
			sh.Episodes[i].File = nil
		}
	}
	return sh
}

func fixWord(info releasename.Info) string {
	if info.Repack {
		return "REPACK"
	}
	return "PROPER"
}
