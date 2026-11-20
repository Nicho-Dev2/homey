package shows

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/downloads"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
)

// maxEpisodeSearches caps how many single-episode searches one "search
// everything missing" makes, to go easy on the indexers. The rest wait for
// the next search.
const maxEpisodeSearches = 20

// parallelSearches is how many episode searches run at once. Each indexer
// still spaces out its own requests (its definition's requestDelay).
const parallelSearches = 4

// Releases searches every indexer for a target and judges each result.
func (s *Service) Releases(ctx context.Context, id int, t Target) ([]Decision, []string, error) {
	sh, ok := s.Get(id)
	if !ok {
		return nil, nil, errNotFound
	}
	res := s.Indexers.Search(ctx, model.Query{
		Kind: "tv", Text: sh.Title, Season: t.Season, Episode: t.Episode, Categories: []int{model.CategoryTV},
	})
	var failures []string
	for _, f := range res.Failures {
		failures = append(failures, f.IndexerName+": "+f.Error)
	}
	count := 1
	if t.Episode == 0 {
		count = len(episodesIn(sh, t.Season))
	}
	return Decide(sh, t, count, s.Store.Read().Settings.Shows, res.Releases), failures, nil
}

func episodesIn(sh store.Show, season int) []store.Episode {
	var out []store.Episode
	for _, e := range sh.Episodes {
		if e.Season == season {
			out = append(out, e)
		}
	}
	return out
}

// Missing lists the followed episodes that have aired, have no file and
// aren't downloading.
func (s *Service) Missing(sh store.Show) []store.Episode {
	var out []store.Episode
	for _, e := range sh.Episodes {
		if e.Monitored && e.File == nil && Aired(e, s.Now()) && !downloading(sh, e) {
			out = append(out, e)
		}
	}
	return out
}

func downloading(sh store.Show, e store.Episode) bool {
	for _, d := range sh.Downloads {
		if d.Season == e.Season && (d.Episode == 0 || d.Episode == e.Number) {
			return true
		}
	}
	return false
}

// SearchAndGrab searches for a target, or, with a zero target, for every
// missing episode. A season target tries a season pack first and, if none
// passes, each missing episode of the season. Searches for different
// episodes can run at the same time; the same one can't run twice. It
// returns a message for the owner and saves it as the show's last search.
func (s *Service) SearchAndGrab(ctx context.Context, id int, t Target) (string, error) {
	key := fmt.Sprintf("%d:%s", id, t.String())
	if t.Season == 0 {
		key = fmt.Sprintf("%d:all", id)
	}
	if _, running := s.busy.LoadOrStore(key, true); running {
		return "", errors.New("Already searching for this.")
	}
	defer s.busy.Delete(key)

	sh, ok := s.Get(id)
	if !ok {
		return "", errNotFound
	}
	var msg string
	switch {
	case t.Season == 0:
		msg = s.searchMissing(ctx, sh)
	case t.Episode == 0:
		msg = s.searchSeason(ctx, sh, t.Season)
	default:
		msg = s.searchOne(ctx, sh, t)
	}
	log.Printf("shows: search for %s: %s", sh.Title, msg)
	_ = s.update(id, func(x *store.Show) { x.LastSearch = &store.SearchNote{At: s.Now(), Message: msg} })
	return msg, nil
}

// searchOne searches for one episode and sends the best release.
func (s *Service) searchOne(ctx context.Context, sh store.Show, t Target) string {
	sent, msg := s.tryTarget(ctx, sh, t)
	if sent {
		return "Sent " + msg + " to qBittorrent."
	}
	return t.String() + ": " + msg + "."
}

// searchSeason tries a season pack, then the season's missing episodes one
// by one (in parallel).
func (s *Service) searchSeason(ctx context.Context, sh store.Show, season int) string {
	sent, why := s.tryTarget(ctx, sh, Target{Season: season})
	if sent {
		return "Sent season pack " + why + " to qBittorrent."
	}
	var eps []store.Episode
	for _, e := range s.Missing(sh) {
		if e.Season == season {
			eps = append(eps, e)
		}
	}
	if len(eps) == 0 {
		return fmt.Sprintf("Season %d: no season pack (%s), and no missing episodes that have aired.", season, why)
	}
	singles, notFound := s.searchEpisodes(ctx, sh, eps)
	return fmt.Sprintf("Season %d: no season pack (%s). %s", season, why, summary(0, singles, notFound))
}

// searchEpisodes searches for each episode, a few at a time, and returns
// how many were sent and how many found nothing good.
func (s *Service) searchEpisodes(ctx context.Context, sh store.Show, eps []store.Episode) (sent, notFound int) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	slots := make(chan struct{}, parallelSearches)
	for _, e := range eps {
		t := Target{Season: e.Season, Episode: e.Number}
		key := fmt.Sprintf("%d:%s", sh.TMDBID, t)
		if _, running := s.busy.LoadOrStore(key, true); running {
			continue // already being searched from its own button
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer s.busy.Delete(key)
			slots <- struct{}{}
			defer func() { <-slots }()
			ok, _ := s.tryTarget(ctx, sh, t)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				sent++
			} else {
				notFound++
			}
		}()
	}
	wg.Wait()
	return sent, notFound
}

func summary(packs, singles, notFound int) string {
	parts := []string{}
	if packs > 0 {
		parts = append(parts, plural(packs, "season pack"))
	}
	if singles > 0 {
		parts = append(parts, plural(singles, "episode"))
	}
	msg := "Nothing sent."
	if len(parts) > 0 {
		msg = "Sent " + strings.Join(parts, " and ") + " to qBittorrent."
	}
	if notFound > 0 {
		msg += fmt.Sprintf(" No good release for %s.", plural(notFound, "episode"))
	}
	return msg
}

// tryTarget returns whether a release was sent, and its title or why not.
func (s *Service) tryTarget(ctx context.Context, sh store.Show, t Target) (bool, string) {
	decisions, failures, err := s.Releases(ctx, sh.TMDBID, t)
	switch {
	case err != nil:
		return false, err.Error()
	case len(decisions) == 0 && len(failures) > 0:
		return false, "no indexer answered (" + strings.Join(failures, "; ") + ")"
	case len(decisions) == 0:
		return false, "no releases found"
	case !decisions[0].Accepted:
		lists := make([][]string, len(decisions))
		for i, d := range decisions {
			lists[i] = d.Reasons
		}
		return false, fmt.Sprintf("%d releases, none passed (mostly: %s)", len(decisions), library.CommonReason(lists))
	}
	best := decisions[0].Release
	if err := s.Grab(ctx, sh.TMDBID, t, best); err != nil {
		return false, "couldn't send the release: " + err.Error()
	}
	return true, best.Title
}

// searchMissing searches for everything missing.
func (s *Service) searchMissing(ctx context.Context, sh store.Show) string {
	missing := s.Missing(sh)
	if len(missing) == 0 {
		return "Nothing missing: every followed episode that has aired is downloaded or downloading."
	}
	return s.searchList(ctx, sh, missing)
}

// searchList searches for the given episodes: a season pack first for any
// season where every episode has aired and none has a file, then the rest
// one by one, in parallel.
func (s *Service) searchList(ctx context.Context, sh store.Show, missing []store.Episode) string {
	bySeason := map[int][]store.Episode{}
	var seasons []int
	for _, e := range missing {
		if bySeason[e.Season] == nil {
			seasons = append(seasons, e.Season)
		}
		bySeason[e.Season] = append(bySeason[e.Season], e)
	}

	packs := 0
	var singles []store.Episode
	for _, season := range seasons {
		eps := bySeason[season]
		all := episodesIn(sh, season)
		if len(eps) == len(all) && len(all) > 1 && s.seasonAired(all) && noFiles(all) {
			if sent, _ := s.tryTarget(ctx, sh, Target{Season: season}); sent {
				packs++
				continue
			}
		}
		singles = append(singles, eps...)
	}
	waiting := 0
	if len(singles) > maxEpisodeSearches {
		waiting = len(singles) - maxEpisodeSearches
		singles = singles[:maxEpisodeSearches]
	}
	sent, notFound := s.searchEpisodes(ctx, sh, singles)
	msg := summary(packs, sent, notFound)
	if waiting > 0 {
		msg += fmt.Sprintf(" %d more wait for the next search.", waiting)
	}
	return msg
}

func (s *Service) seasonAired(eps []store.Episode) bool {
	for _, e := range eps {
		if !Aired(e, s.Now()) {
			return false
		}
	}
	return true
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Grab sends one release for a target to qBittorrent, tagged so the app can
// follow it, and records the download.
func (s *Service) Grab(ctx context.Context, id int, t Target, r model.Release) error {
	if t.Season < 1 {
		return errors.New("Choose a season.")
	}
	// One grab at a time, so two searches running side by side can't both
	// send the same episode (a season pack and one of its episodes, say).
	s.grabMu.Lock()
	defer s.grabMu.Unlock()
	sh, ok := s.Get(id)
	if !ok {
		return errNotFound
	}
	if covered(sh, t) {
		return errors.New("already downloading")
	}
	link, err := s.Indexers.DownloadLink(ctx, r.IndexerID, r.DownloadURL, r.MagnetURL)
	if err != nil {
		return err
	}
	tag := Tag(sh, t)
	kind := downloads.LinkKind(link)
	if err := downloads.Send(ctx, s.Store.Read().Settings, link, tag, r.Size); err != nil {
		log.Printf("shows: sending %s for %s: %v", r.Title, sh.Title, err)
		return err
	}
	log.Printf("shows: sent %s for %s from %s as a %s", r.Title, sh.Title, r.IndexerName, kind)
	return s.update(id, func(x *store.Show) {
		x.Downloads = append(x.Downloads, store.ShowDownload{
			Download: store.Download{Tag: tag, ReleaseTitle: r.Title, IndexerName: r.IndexerName, Link: kind, SentAt: s.Now(),
				Hash: r.InfoHash, Size: r.Size, State: "sent"},
			Season: t.Season, Episode: t.Episode,
		})
	})
}

// Tag marks a show download in qBittorrent: oneforarr-show-<id>-s01 for a
// season, -s01e02 for an episode.
func Tag(sh store.Show, t Target) string {
	tag := fmt.Sprintf("oneforarr-show-%d-s%02d", sh.TMDBID, t.Season)
	if t.Episode > 0 {
		tag += fmt.Sprintf("e%02d", t.Episode)
	}
	return tag
}

// covered reports whether a download already covers the target: the same
// episode, or the season pack it belongs to.
func covered(sh store.Show, t Target) bool {
	for _, d := range sh.Downloads {
		if d.Season == t.Season && (d.Episode == 0 || d.Episode == t.Episode) {
			return true
		}
	}
	return false
}

func noFiles(eps []store.Episode) bool {
	for _, e := range eps {
		if e.File != nil {
			return false
		}
	}
	return true
}
