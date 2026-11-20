// Package library holds what Movies and Shows share: quality presets, the
// checks every release must pass, and putting files into the library.
package library

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/releasename"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
)

// ---- Quality ----

// Preset is a quality choice: which resolutions are allowed and which is
// wanted most.
type Preset struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Allowed   []int  `json:"allowed"` // resolutions; empty = any, including unknown
	Preferred int    `json:"preferred"`
}

// Presets are the quality choices for a movie or show.
var Presets = []Preset{
	{ID: "1080p", Label: "HD 1080p (720p–1080p)", Allowed: []int{720, 1080}, Preferred: 1080},
	{ID: "2160p", Label: "4K (1080p–2160p)", Allowed: []int{1080, 2160}, Preferred: 2160},
	{ID: "720p", Label: "HD 720p (SD–720p)", Allowed: []int{480, 576, 720}, Preferred: 720},
	{ID: "any", Label: "Any quality", Preferred: 2160},
}

// PresetByID returns a preset, or the 1080p one for an unknown id.
func PresetByID(id string) Preset {
	for _, p := range Presets {
		if p.ID == id {
			return p
		}
	}
	return Presets[0]
}

// ValidPreset reports whether id names a preset.
func ValidPreset(id string) bool {
	for _, p := range Presets {
		if p.ID == id {
			return true
		}
	}
	return false
}

// Allows reports whether the preset accepts a resolution.
func (p Preset) Allows(res int) bool {
	if len(p.Allowed) == 0 {
		return true
	}
	for _, r := range p.Allowed {
		if r == res {
			return true
		}
	}
	return false
}

// HealthySeeders is how many seeders make a download reasonably fast.
const HealthySeeders = 5

// MinSize is the smallest believable video release; smaller ones are
// samples or fakes.
const MinSize = 100 << 20

// CommonChecks rejects releases for reasons that don't depend on what's
// being looked for: cinema copies, 3D, quality outside the preset, too few
// seeders. It returns the reasons (empty = fine).
func CommonChecks(info releasename.Info, preset Preset, seeders, minSeeders int) []string {
	var reasons []string
	if info.Source.Cinema() {
		reasons = append(reasons, fmt.Sprintf("Recorded in a cinema (%s)", info.Source))
	}
	if info.ThreeD {
		reasons = append(reasons, "3D")
	}
	switch {
	case info.Resolution == 0 && len(preset.Allowed) > 0:
		reasons = append(reasons, "Quality unknown")
	case !preset.Allows(info.Resolution):
		reasons = append(reasons, fmt.Sprintf("%dp isn't in %s", info.Resolution, preset.Label))
	}
	if seeders < minSeeders {
		reasons = append(reasons, fmt.Sprintf("Only %d seeders (need %d)", seeders, minSeeders))
	}
	return reasons
}

// Score ranks acceptable releases: the preferred resolution first, then
// enough seeders, then the better source, then more seeders.
func Score(info releasename.Info, preset Preset, seeders int) float64 {
	s := 0.0
	if info.Resolution == preset.Preferred {
		s += 10000
	}
	s += float64(info.Resolution) // a higher resolution breaks ties ("any")
	if seeders >= HealthySeeders {
		s += 5000
	}
	s += float64(info.Source) * 100
	s += math.Log10(float64(seeders)+1) * 10
	if info.Proper {
		s += 5
	}
	return s
}

// FileResolution is a library file's resolution: saved with it, or read
// from its quality ("Bluray-1080p") or name.
func FileResolution(f *store.MediaFile) int {
	if f == nil {
		return 0
	}
	if f.Resolution > 0 {
		return f.Resolution
	}
	if m := reRes.FindStringSubmatch(f.Quality + " " + filepath.Base(f.Path)); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

var reRes = regexp.MustCompile(`(?i)(480|576|720|1080|2160)p`)

// UpgradeCheck says why a release can't replace the file already there
// ("" = it can). Upgrades go up to the preset's preferred resolution and
// stop there.
func UpgradeCheck(have *store.MediaFile, upgrades bool, preset Preset, info releasename.Info) string {
	if have == nil {
		return ""
	}
	res := FileResolution(have)
	switch {
	case !upgrades:
		return "Already downloaded (upgrades are off)"
	case res >= preset.Preferred:
		return fmt.Sprintf("Already have %s, the quality you want", have.Quality)
	case info.Resolution <= res:
		return fmt.Sprintf("Not better than what you have (%s)", have.Quality)
	}
	return ""
}

// Wanted reports whether a file is missing or, with upgrades on, below the
// preferred resolution.
func Wanted(have *store.MediaFile, upgrades bool, preset Preset) bool {
	return have == nil || (upgrades && FileResolution(have) < preset.Preferred)
}

// TitleMatches reports whether a release's title is one of the names.
func TitleMatches(releaseTitle string, names ...string) bool {
	n := releasename.Normalize(releaseTitle)
	if n == "" {
		return false
	}
	for _, t := range names {
		if t != "" && n == releasename.Normalize(t) {
			return true
		}
	}
	return false
}

// CommonReason is the rejection reason most releases share, so the owner
// sees what to change. "Too big (61 GB, ...)" and "Too big (58 GB, ...)"
// count as the same reason.
func CommonReason(reasonLists [][]string) string {
	count := map[string]int{}
	best, bestN := "", 0
	for _, reasons := range reasonLists {
		for _, r := range reasons {
			key, _, _ := strings.Cut(r, " (")
			count[key]++
			if count[key] > bestN {
				best, bestN = key, count[key]
			}
		}
	}
	return strings.ToLower(best)
}

// ---- Files ----

var videoExt = map[string]bool{".mkv": true, ".mp4": true, ".avi": true, ".m4v": true, ".mov": true, ".ts": true, ".wmv": true}

// VideoFile is one video file found on disk.
type VideoFile struct {
	Path string
	Size int64
}

// VideoFiles lists the video files at path (a file or a folder), largest
// first, leaving out samples.
func VideoFiles(path string) []VideoFile {
	var out []VideoFile
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := strings.ToLower(filepath.Base(p))
		if !videoExt[strings.ToLower(filepath.Ext(p))] || strings.Contains(name, "sample") {
			return nil
		}
		if info, err := d.Info(); err == nil {
			out = append(out, VideoFile{Path: p, Size: info.Size()})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out
}

// CleanFileName makes a title safe as a file or folder name.
func CleanFileName(s string) string {
	s = strings.NewReplacer(":", " -", "/", "-", "\\", "-", "?", "", "*", "", "\"", "'", "<", "", ">", "", "|", "-").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	return strings.Trim(s, ". ")
}

// Place puts src at dst: a hardlink, so it takes no extra space and the
// torrent keeps seeding; a copy when they're on different drives. An
// existing dst that is already the same file is fine.
func Place(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if same, _ := sameFile(src, dst); same {
		return nil
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%s already exists", dst)
	}
	err := os.Link(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return fmt.Errorf("linking the file: %w", err)
	}
	// Different drives: hardlinks can't cross them, so copy.
	log.Printf("library: %s is on another drive than the library; copying (uses extra space)", src)
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("copying the file: %w", err)
	}
	return nil
}

func sameFile(a, b string) (bool, error) {
	ia, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ia, ib), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".partial"
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// RemoveFolder deletes dir, but only if it sits directly inside root.
func RemoveFolder(root, dir string) error {
	if filepath.Dir(filepath.Clean(dir)) != filepath.Clean(root) || filepath.Base(dir) == "" {
		return errors.New("the folder isn't inside the library folder; nothing was deleted")
	}
	return os.RemoveAll(dir)
}
