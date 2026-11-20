package movies

import (
	"context"
	"errors"
	"log"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/downloads"
)

// Cancel stops the movie's download: the torrent and its partial files are
// removed from qBittorrent and that release is never picked again. The
// movie stays wanted, so a later search may pick another release; remove
// the movie to stop that.
func (s *Service) Cancel(ctx context.Context, id int) error {
	m, ok := s.Get(id)
	if !ok {
		return errNotFound
	}
	if m.Download == nil {
		return errors.New("Nothing is downloading for this movie.")
	}
	d := *m.Download
	if err := downloads.Remove(ctx, s.Store.Read().Settings, d.Tag); err != nil {
		return err
	}
	log.Printf("movies: cancelled %s for %s", d.ReleaseTitle, m.Title)
	return s.update(id, func(x *store.Movie) {
		x.Download = nil
		x.Blocked = append(x.Blocked, d.ReleaseTitle)
		x.LastSearch = &store.SearchNote{At: s.Now(), Message: "Cancelled " + d.ReleaseTitle + "."}
	})
}
