package movies

import (
	"fmt"
	"slices"
	"sort"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/releasename"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
)

// Decision is what the app thinks of one release for one movie.
type Decision struct {
	Release  model.Release    `json:"release"`
	Parsed   releasename.Info `json:"parsed"`
	Quality  string           `json:"quality"`
	Accepted bool             `json:"accepted"`
	// Reasons say why a release was rejected (empty when accepted).
	Reasons []string `json:"reasons"`
	score   float64
}

// Decide judges every release for the movie and returns them best first:
// accepted ones by score, then rejected ones.
func Decide(m store.Movie, rules store.MovieRules, releases []model.Release) []Decision {
	preset := library.PresetByID(m.Quality)
	out := make([]Decision, 0, len(releases))
	for _, r := range releases {
		out = append(out, judge(m, rules, preset, r))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Accepted != out[j].Accepted {
			return out[i].Accepted
		}
		return out[i].score > out[j].score
	})
	return out
}

func judge(m store.Movie, rules store.MovieRules, preset library.Preset, r model.Release) Decision {
	info := releasename.Parse(r.Title)
	d := Decision{Release: r, Parsed: info, Quality: info.Quality(), Reasons: []string{}}
	reject := func(format string, a ...any) { d.Reasons = append(d.Reasons, fmt.Sprintf(format, a...)) }

	if slices.Contains(m.Blocked, r.Title) {
		reject("Gave up on this release before (no seeders)")
	}

	// Is it this movie? An IMDb id from the indexer settles it; otherwise
	// the name must match the title and year.
	sameID := m.IMDBID != "" && r.IMDBID == m.IMDBID
	if !sameID {
		if !library.TitleMatches(info.Title, m.Title, m.OriginalTitle) {
			reject("Different title (%q)", info.Title)
		}
		switch {
		case info.Year == 0:
			reject("No year in the name")
		case m.Year != 0 && abs(info.Year-m.Year) > 1:
			reject("Different year (%d)", info.Year)
		}
	}
	if info.Season > 0 || info.Episode > 0 {
		reject("Looks like a TV episode")
	}
	d.Reasons = append(d.Reasons, library.CommonChecks(info, preset, r.Seeders, rules.MinSeeders)...)
	if rules.MaxSizeGB > 0 && float64(r.Size) > rules.MaxSizeGB*(1<<30) {
		reject("Too big (%.1f GB, limit %g GB)", float64(r.Size)/(1<<30), rules.MaxSizeGB)
	}
	if r.Size > 0 && r.Size < library.MinSize {
		reject("Too small (%d MB)", r.Size>>20)
	}
	if why := library.UpgradeCheck(m.File, rules.Upgrades, preset, info); why != "" {
		reject("%s", why)
	}
	d.Accepted = len(d.Reasons) == 0
	d.score = library.Score(info, preset, r.Seeders)
	return d
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
