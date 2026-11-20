package movies

import (
	"context"
	"log"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
)

// The scheduled work for movies: a regular search for each wanted movie,
// and matching the indexers' newest releases (RSS) against wanted movies.

// Wanted reports whether the app should look for a release for the movie:
// it isn't downloading, and it has no file or (with upgrades on) one below
// the preferred resolution.
func (s *Service) Wanted(m store.Movie) bool {
	rules := s.Store.Read().Settings.Movies
	return m.Download == nil && library.Wanted(m.File, rules.Upgrades, library.PresetByID(m.Quality))
}

// AutoSearchNext searches for the wanted movie that has waited longest
// since its last scheduled search, if one is due (every `every`). It
// returns whether it searched.
func (s *Service) AutoSearchNext(ctx context.Context, every time.Duration) bool {
	var next *store.Movie
	for _, m := range s.Store.Read().Movies {
		if !s.Wanted(m) || s.Now().Sub(m.AutoSearchedAt) < every {
			continue
		}
		if next == nil || m.AutoSearchedAt.Before(next.AutoSearchedAt) {
			m := m
			next = &m
		}
	}
	if next == nil {
		return false
	}
	// Marked first, so a search that fails isn't retried on every tick.
	_ = s.update(next.TMDBID, func(m *store.Movie) { m.AutoSearchedAt = s.Now() })
	msg, err := s.SearchAndGrab(ctx, next.TMDBID)
	if err != nil {
		log.Printf("movies: scheduled search for %s: %v", next.Title, err)
	} else {
		log.Printf("movies: scheduled search for %s: %s", next.Title, msg)
	}
	return true
}

// MatchNewReleases checks the indexers' newest releases against every
// wanted movie and sends the best one that passes. It returns how many
// were sent.
func (s *Service) MatchNewReleases(ctx context.Context, releases []model.Release) int {
	sent := 0
	rules := s.Store.Read().Settings.Movies
	for _, m := range s.Store.Read().Movies {
		if !s.Wanted(m) {
			continue
		}
		ds := Decide(m, rules, releases)
		if len(ds) == 0 || !ds[0].Accepted {
			continue
		}
		best := ds[0].Release
		if err := s.Grab(ctx, m.TMDBID, best); err != nil {
			log.Printf("movies: sending new release %s: %v", best.Title, err)
			continue
		}
		sent++
		log.Printf("movies: new release for %s: %s", m.Title, best.Title)
		_ = s.update(m.TMDBID, func(x *store.Movie) {
			x.LastSearch = &store.SearchNote{At: s.Now(), Message: "Found in the newest releases: sent " + best.Title + " to qBittorrent."}
		})
	}
	return sent
}
