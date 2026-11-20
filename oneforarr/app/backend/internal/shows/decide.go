package shows

import (
	"fmt"
	"regexp"
	"slices"
	"sort"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/releasename"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
)

// Target is what a search looks for: one episode, or a whole season
// (Episode 0).
type Target struct {
	Season  int `json:"season"`
	Episode int `json:"episode"`
}

func (t Target) String() string {
	if t.Episode == 0 {
		return fmt.Sprintf("season %d", t.Season)
	}
	return fmt.Sprintf("S%02dE%02d", t.Season, t.Episode)
}

// Decision is what the app thinks of one release for a target.
type Decision struct {
	Release  model.Release    `json:"release"`
	Parsed   releasename.Info `json:"parsed"`
	Quality  string           `json:"quality"`
	Accepted bool             `json:"accepted"`
	Reasons  []string         `json:"reasons"`
	score    float64
}

// Decide judges every release for the target and returns them best first.
// episodes is how many episodes the target covers (for the size limit).
func Decide(sh store.Show, t Target, episodes int, rules store.ShowRules, releases []model.Release) []Decision {
	preset := library.PresetByID(sh.Quality)
	out := make([]Decision, 0, len(releases))
	for _, r := range releases {
		out = append(out, judge(sh, t, episodes, rules, preset, r))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Accepted != out[j].Accepted {
			return out[i].Accepted
		}
		return out[i].score > out[j].score
	})
	return out
}

func judge(sh store.Show, t Target, episodes int, rules store.ShowRules, preset library.Preset, r model.Release) Decision {
	info := releasename.Parse(r.Title)
	d := Decision{Release: r, Parsed: info, Quality: info.Quality(), Reasons: []string{}}
	reject := func(format string, a ...any) { d.Reasons = append(d.Reasons, fmt.Sprintf(format, a...)) }

	if slices.Contains(sh.Blocked, r.Title) {
		reject("Gave up on this release before (no seeders)")
	}
	if !showTitleMatches(info.Title, sh) {
		reject("Different show (%q)", info.Title)
	}
	// A year in the name tells remakes apart ("Doctor Who 2005").
	if info.Year != 0 && sh.Year != 0 && abs(info.Year-sh.Year) > 1 {
		reject("Different year (%d)", info.Year)
	}
	switch {
	case info.Season == 0:
		reject("No season in the name")
	case info.Season != t.Season:
		reject("Different season (S%02d)", info.Season)
	}
	switch {
	case t.Episode == 0 && info.Episode != 0:
		reject("A single episode, not the whole season")
	case t.Episode != 0 && info.Episode == 0 && info.Season != 0:
		reject("A whole season, not one episode")
	case t.Episode != 0 && info.Episode != 0 && info.Episode != t.Episode:
		reject("Different episode (E%02d)", info.Episode)
	}
	d.Reasons = append(d.Reasons, library.CommonChecks(info, preset, r.Seeders, rules.MinSeeders)...)
	if limit := rules.MaxEpisodeGB * float64(max(episodes, 1)); rules.MaxEpisodeGB > 0 && float64(r.Size) > limit*(1<<30) {
		reject("Too big (%.1f GB, limit %g GB)", float64(r.Size)/(1<<30), limit)
	}
	if r.Size > 0 && r.Size < 50<<20 {
		reject("Too small (%d MB)", r.Size>>20)
	}
	if t.Episode != 0 {
		if e, ok := findEpisode(sh, t.Season, t.Episode); ok {
			if why := library.UpgradeCheck(e.File, rules.Upgrades, preset, info); why != "" {
				reject("%s", why)
			}
		}
	}
	d.Accepted = len(d.Reasons) == 0
	d.score = library.Score(info, preset, r.Seeders)
	return d
}

// countrySuffix matches a country after a show's name, as release names
// tell versions apart: "The Office US", "Shameless UK".
var countrySuffix = regexp.MustCompile(`(?i)\s+(us|uk|au|ca|nz)$`)

// showTitleMatches compares a release's show name with the show's title,
// also without a trailing country.
func showTitleMatches(name string, sh store.Show) bool {
	if library.TitleMatches(name, sh.Title, sh.OriginalTitle) {
		return true
	}
	if trimmed := countrySuffix.ReplaceAllString(name, ""); trimmed != name {
		return library.TitleMatches(trimmed, sh.Title, sh.OriginalTitle)
	}
	return false
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
