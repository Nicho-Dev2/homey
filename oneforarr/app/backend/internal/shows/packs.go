package shows

import (
	"context"
	"log"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/downloads"
)

// Season packs or single episodes?
//
// Old seasons are usually shared as a whole: a season pack has many more
// seeders than its single episodes. So once a season has fully aired and
// at least packMinMissing followed episodes still need a file, the app
// looks for a pack first, even if some episodes are already in the library
// or downloading on their own. When it sends a pack, it cancels the
// season's single downloads that aren't worth keeping, and the pack fills
// only the episodes still needed (see importDownload).

// packMinMissing is how many episodes a season must be missing before a
// season pack is worth it. Fewer than that are fetched one by one.
const packMinMissing = 3

// keepSingleAt: a single-episode download this far along, and still
// moving, is kept when a season pack is sent for its season.
const keepSingleAt = 0.5

// packWorthIt reports whether to look for a season pack: every episode of
// the season has aired, no pack is already downloading, and at least
// packMinMissing followed episodes have no file and no single download
// worth keeping.
func (s *Service) packWorthIt(sh store.Show, season int) bool {
	all := episodesIn(sh, season)
	if len(all) < packMinMissing || !s.seasonAired(all) {
		return false
	}
	for _, d := range sh.Downloads {
		if d.Season == season && d.Episode == 0 {
			return false // a pack is already on its way
		}
	}
	missing := 0
	for _, e := range all {
		if e.Monitored && e.File == nil && !hasGoodSingle(sh, e) {
			missing++
		}
	}
	return missing >= packMinMissing
}

// worthKeeping says whether a single-episode download should keep going
// when a season pack is sent: it's at least half done and not stuck.
func worthKeeping(d store.ShowDownload) bool {
	return d.Progress >= keepSingleAt && d.StuckSince.IsZero()
}

func hasGoodSingle(sh store.Show, e store.Episode) bool {
	for _, d := range sh.Downloads {
		if d.Season == e.Season && d.Episode == e.Number && worthKeeping(d) {
			return true
		}
	}
	return false
}

// dropWeakSingles cancels a season's single-episode downloads that aren't
// worth keeping, now that a season pack is coming. The episodes stay
// followed and their releases aren't blocked; the pack covers them.
func (s *Service) dropWeakSingles(ctx context.Context, id, season int) {
	sh, ok := s.Get(id)
	if !ok {
		return
	}
	set := s.Store.Read().Settings
	var dropped []string
	for _, d := range sh.Downloads {
		if d.Season != season || d.Episode == 0 || worthKeeping(d) {
			continue
		}
		if err := downloads.Remove(ctx, set, d.Tag); err != nil {
			log.Printf("shows: cancelling %s for the season pack: %v", d.ReleaseTitle, err)
			continue
		}
		log.Printf("shows: cancelled %s for %s; the season pack covers it", d.ReleaseTitle, sh.Title)
		dropped = append(dropped, d.Tag)
	}
	if len(dropped) == 0 {
		return
	}
	_ = s.update(id, func(x *store.Show) {
		for _, tag := range dropped {
			x.Downloads = without(x.Downloads, tag)
		}
	})
}
