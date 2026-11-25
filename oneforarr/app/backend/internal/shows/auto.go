package shows

import (
	"context"
	"log"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/releasename"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
)

// The scheduled work for shows: a regular search for each show with wanted
// episodes, and matching the indexers' newest releases (RSS) against them.

// Wanted lists the episodes the app should look for: followed, aired, not
// downloading, and with no file or (with upgrades on) one below the
// preferred resolution.
func (s *Service) Wanted(sh store.Show) []store.Episode {
	rules := s.Store.Read().Settings.Shows
	preset := library.PresetByID(sh.Quality)
	var out []store.Episode
	for _, e := range sh.Episodes {
		if e.Monitored && Aired(e, s.Now()) && !downloading(sh, e) && library.Wanted(e.File, rules.Upgrades, preset) {
			out = append(out, e)
		}
	}
	return out
}

// AutoSearchNext searches for the show that has waited longest, if one is
// due: its last scheduled search is older than `every`, or a wanted episode
// has aired since. It returns whether it searched.
func (s *Service) AutoSearchNext(ctx context.Context, every time.Duration) bool {
	var next *store.Show
	var wanted []store.Episode
	for _, sh := range s.Store.Read().Shows {
		w := s.Wanted(sh)
		if len(w) == 0 || !s.due(sh, w, every) {
			continue
		}
		if next == nil || sh.AutoSearchedAt.Before(next.AutoSearchedAt) {
			sh := sh
			next, wanted = &sh, w
		}
	}
	if next == nil {
		return false
	}
	_ = s.update(next.TMDBID, func(x *store.Show) { x.AutoSearchedAt = s.Now() })
	key := "auto:" + next.Title
	if _, running := s.busy.LoadOrStore(key, true); running {
		return false
	}
	defer s.busy.Delete(key)
	msg := s.searchList(ctx, *next, wanted)
	log.Printf("shows: scheduled search for %s: %s", next.Title, msg)
	_ = s.update(next.TMDBID, func(x *store.Show) { x.LastSearch = &store.SearchNote{At: s.Now(), Message: msg} })
	return true
}

func (s *Service) due(sh store.Show, wanted []store.Episode, every time.Duration) bool {
	if s.Now().Sub(sh.AutoSearchedAt) >= every {
		return true
	}
	for _, e := range wanted {
		aired := e.AirStamp
		if aired.IsZero() {
			aired, _ = time.Parse("2006-01-02", e.AirDate)
		}
		if aired.After(sh.AutoSearchedAt) {
			return true // aired since the last scheduled search
		}
	}
	return false
}

// MatchNewReleases checks the indexers' newest releases against every
// show's wanted episodes (and season packs, where one is worth it; see
// packWorthIt), and sends the best release for each. It returns how many
// were sent.
func (s *Service) MatchNewReleases(ctx context.Context, releases []model.Release) int {
	sent := 0
	rules := s.Store.Read().Settings.Shows
	for _, sh := range s.Store.Read().Shows {
		sent += s.grabFixes(ctx, sh, releases)
		wanted := map[Target]bool{}
		for _, e := range s.Wanted(sh) {
			wanted[Target{Season: e.Season, Episode: e.Number}] = true
		}
		if len(wanted) == 0 {
			continue
		}
		// Which releases might be for which target.
		groups := map[Target][]model.Release{}
		for _, r := range releases {
			info := releasename.Parse(r.Title)
			if info.Season == 0 || !showTitleMatches(info.Title, sh) {
				continue
			}
			t := Target{Season: info.Season, Episode: info.Episode}
			if t.Episode == 0 && !s.packWorthIt(sh, t.Season) {
				continue
			}
			if t.Episode != 0 && !wanted[t] {
				continue
			}
			groups[t] = append(groups[t], r)
		}
		// Season packs first, so their episodes are then covered.
		for _, packsFirst := range []bool{true, false} {
			for t, group := range groups {
				if (t.Episode == 0) != packsFirst {
					continue
				}
				current, _ := s.Get(sh.TMDBID)
				count := 1
				if t.Episode == 0 {
					count = len(episodesIn(current, t.Season))
				}
				ds := Decide(current, t, count, rules, group)
				if len(ds) == 0 || !ds[0].Accepted {
					continue
				}
				best := ds[0].Release
				if err := s.Grab(ctx, sh.TMDBID, t, best); err != nil {
					continue // covered by a pack already, or qBittorrent refused
				}
				sent++
				log.Printf("shows: new release for %s %s: %s", sh.Title, t, best.Title)
				_ = s.update(sh.TMDBID, func(x *store.Show) {
					x.LastSearch = &store.SearchNote{At: s.Now(), Message: "Found in the newest releases: sent " + best.Title + " to qBittorrent."}
				})
			}
		}
	}
	return sent
}
