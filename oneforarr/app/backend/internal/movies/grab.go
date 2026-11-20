package movies

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/downloads"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
)

// Releases searches every indexer for the movie and judges each result.
// failures lists indexers that couldn't answer.
//
// replacing judges them as if the movie had no file yet, so releases of the
// same quality are offered to replace a bad copy.
func (s *Service) Releases(ctx context.Context, id int, replacing bool) ([]Decision, []string, error) {
	m, ok := s.Get(id)
	if !ok {
		return nil, nil, errNotFound
	}
	if replacing {
		m.File = nil
	}
	text := m.Title
	if m.Year > 0 {
		text += " " + strconv.Itoa(m.Year)
	}
	res := s.Indexers.Search(ctx, model.Query{
		Kind: "movie", Text: text, IMDBID: m.IMDBID, Categories: []int{model.CategoryMovies},
	})
	var failures []string
	for _, f := range res.Failures {
		failures = append(failures, f.IndexerName+": "+f.Error)
	}
	return Decide(m, s.Store.Read().Settings.Movies, res.Releases), failures, nil
}

// SearchAndGrab looks for the movie and sends the best release to
// qBittorrent. It returns a message for the owner either way, and saves it
// as the movie's last search.
func (s *Service) SearchAndGrab(ctx context.Context, id int) (string, error) {
	if _, running := s.busy.LoadOrStore(id, true); running {
		return "", errors.New("A search for this movie is already running.")
	}
	defer s.busy.Delete(id)

	m, ok := s.Get(id)
	if !ok {
		return "", errNotFound
	}
	if m.Download != nil {
		return "", errors.New("This movie is already downloading.")
	}
	decisions, failures, err := s.Releases(ctx, id, false)
	if err != nil {
		return "", err
	}
	var msg string
	var grabErr error
	switch {
	case len(decisions) == 0 && len(failures) > 0:
		msg = "No indexer answered: " + strings.Join(failures, "; ")
	case len(decisions) == 0:
		msg = "No releases found."
	case !decisions[0].Accepted:
		msg = fmt.Sprintf("Found %d releases, but none passed. Most common reason: %s.", len(decisions), library.CommonReason(reasonsOf(decisions)))
	default:
		best := decisions[0]
		if grabErr = s.Grab(ctx, id, best.Release); grabErr == nil {
			msg = "Sent " + best.Release.Title + " to qBittorrent."
		} else {
			msg = "Couldn't send the release: " + grabErr.Error()
		}
	}
	log.Printf("movies: search for %s (%d): %s", m.Title, m.Year, msg)
	_ = s.update(id, func(m *store.Movie) { m.LastSearch = &store.SearchNote{At: s.Now(), Message: msg} })
	return msg, grabErr
}

// Grab sends one release to qBittorrent for the movie, tagged so the app
// can follow it, and records it as the movie's download.
func (s *Service) Grab(ctx context.Context, id int, r model.Release) error {
	m, ok := s.Get(id)
	if !ok {
		return errNotFound
	}
	link, err := s.Indexers.DownloadLink(ctx, r.IndexerID, r.DownloadURL, r.MagnetURL)
	if err != nil {
		return err
	}
	tag := Tag(m)
	kind := downloads.LinkKind(link)
	if err := downloads.Send(ctx, s.Store.Read().Settings, link, tag, r.Size); err != nil {
		log.Printf("movies: sending %s for %s: %v", r.Title, m.Title, err)
		return err
	}
	log.Printf("movies: sent %s for %s (%d) from %s as a %s", r.Title, m.Title, m.Year, r.IndexerName, kind)
	return s.update(id, func(m *store.Movie) {
		m.Download = &store.Download{
			Tag: tag, ReleaseTitle: r.Title, IndexerName: r.IndexerName, Link: kind, SentAt: s.Now(),
			Hash: r.InfoHash, Size: r.Size, State: "sent",
		}
	})
}

// Tag is the qBittorrent tag that marks a movie's download.
func Tag(m store.Movie) string { return "oneforarr-movie-" + strconv.Itoa(m.TMDBID) }

func reasonsOf(ds []Decision) [][]string {
	out := make([][]string, len(ds))
	for i, d := range ds {
		out[i] = d.Reasons
	}
	return out
}
