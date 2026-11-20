package shows

import (
	"context"
	"errors"
	"log"
	"slices"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/downloads"
)

// Cancel stops one of the show's downloads: the torrent and its partial
// files are removed from qBittorrent, and the episodes it was for are no
// longer followed, so the app won't download them again on its own.
// (Follow them again on the show's page to change that.)
func (s *Service) Cancel(ctx context.Context, id int, tag string) error {
	sh, ok := s.Get(id)
	if !ok {
		return errNotFound
	}
	i := slices.IndexFunc(sh.Downloads, func(d store.ShowDownload) bool { return d.Tag == tag })
	if i < 0 {
		return errors.New("That download isn't running any more.")
	}
	d := sh.Downloads[i]
	if err := downloads.Remove(ctx, s.Store.Read().Settings, tag); err != nil {
		return err
	}
	t := targetOf(d)
	log.Printf("shows: cancelled %s for %s %s", d.ReleaseTitle, sh.Title, t)
	return s.update(id, func(x *store.Show) {
		x.Downloads = without(x.Downloads, tag)
		for j := range x.Episodes {
			e := &x.Episodes[j]
			if e.Season == t.Season && (t.Episode == 0 || e.Number == t.Episode) && e.File == nil {
				e.Monitored = false
			}
		}
		x.LastSearch = &store.SearchNote{At: s.Now(), Message: "Cancelled " + d.ReleaseTitle + "; " + t.String() + " is no longer followed."}
	})
}
