// Package scan reads the Movies and TV folders and brings the app in line
// with what's on the drive: files that are gone are forgotten, files dropped
// in a known movie's or show's folder are picked up, and folders the app
// doesn't know yet are looked up on TMDB.
//
// A folder is added on its own only when TMDB has exactly one movie or show
// with that exact title (and year, when the name has one). Anything else is
// listed as unmatched on the Library page, where the owner picks the right
// one or skips it. Shows found this way follow no episodes, so nothing is
// downloaded for them until the owner chooses to.
//
// Scans run only when asked for (the Scan button on the Library page).
package scan

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/releasename"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/library"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/metadata"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/movies"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/shows"
)

// Service scans the library folders.
type Service struct {
	Store  *store.Store
	Movies *movies.Service
	Shows  *shows.Service
	Now    func() time.Time

	running atomic.Bool
}

// New returns the scanner.
func New(s *store.Store, mov *movies.Service, tv *shows.Service) *Service {
	return &Service{Store: s, Movies: mov, Shows: tv, Now: time.Now}
}

// Running says whether a scan is going on.
func (s *Service) Running() bool { return s.running.Load() }

// Start begins a scan in the background. It returns false when one is
// already running.
func (s *Service) Start() bool {
	if !s.running.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer s.running.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		s.Scan(ctx)
	}()
	return true
}

// tally counts what a scan did, for the one-line report.
type tally struct {
	movies, shows  int // added
	found, missing int // files
	problems       []string
}

// Scan does one full scan and saves what it found. It returns the report.
func (s *Service) Scan(ctx context.Context) string {
	set := s.Store.Read().Settings
	var t tally
	var unmatched []store.Unmatched

	if err := readable(set.Folders.Movies); err != nil {
		t.problems = append(t.problems, "movies folder: "+err.Error())
	} else {
		f, m := s.Movies.CheckFiles()
		t.found, t.missing = t.found+f, t.missing+m
		unmatched = append(unmatched, s.newMovies(ctx, set, &t)...)
	}
	if err := readable(set.Folders.Shows); err != nil {
		t.problems = append(t.problems, "TV folder: "+err.Error())
	} else {
		f, m := s.Shows.CheckFiles()
		t.found, t.missing = t.found+f, t.missing+m
		unmatched = append(unmatched, s.newShows(ctx, set, &t)...)
	}

	report := t.report(len(unmatched))
	_ = s.Store.Update(func(d *store.Data) error {
		d.Library.LastScan = s.Now()
		d.Library.Report = report
		d.Library.Unmatched = unmatched
		return nil
	})
	log.Printf("scan: %s", report)
	return report
}

func (t tally) report(unmatched int) string {
	var parts []string
	var added []string
	if t.movies > 0 {
		added = append(added, plural(t.movies, "movie"))
	}
	if t.shows > 0 {
		added = append(added, plural(t.shows, "show"))
	}
	if len(added) > 0 {
		parts = append(parts, "Added "+strings.Join(added, " and ")+".")
	}
	if t.found > 0 {
		parts = append(parts, fmt.Sprintf("Found %s.", plural(t.found, "new file")))
	}
	if t.missing > 0 {
		parts = append(parts, fmt.Sprintf("Forgot %s no longer on the drive.", plural(t.missing, "file")))
	}
	if unmatched > 0 {
		verb := "need"
		if unmatched == 1 {
			verb = "needs"
		}
		parts = append(parts, fmt.Sprintf("%s %s a match.", plural(unmatched, "item"), verb))
	}
	if len(t.problems) > 0 {
		parts = append(parts, "Couldn't read the "+strings.Join(t.problems, "; ")+".")
	}
	if len(parts) == 0 {
		return "Everything on the drive is already in the app."
	}
	return strings.Join(parts, " ")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// readable checks the folder can be listed. A missing or unplugged drive
// must not make every file look gone.
func readable(dir string) error {
	if dir == "" {
		return errors.New("not set in Settings")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("%s (is the drive connected?)", dir)
	}
	if len(entries) == 0 {
		return fmt.Errorf("%s is empty (is the drive connected?)", dir)
	}
	return nil
}

// ---- Movies ----

// newMovies adds or lists the folders and loose video files in the movies
// folder that no movie uses yet.
func (s *Service) newMovies(ctx context.Context, set store.Settings, t *tally) []store.Unmatched {
	d := s.Store.Read()
	knownDirs := map[string]bool{}
	knownFiles := map[string]bool{}
	for _, m := range d.Movies {
		knownDirs[movies.FolderName(m)] = true
		if m.File != nil {
			knownFiles[m.File.Path] = true
		}
	}
	var out []store.Unmatched
	for _, path := range s.entries(set.Folders.Movies, d.Library.Skipped) {
		name := filepath.Base(path)
		files := library.VideoFiles(path)
		if len(files) == 0 || knownDirs[name] || slices.ContainsFunc(files, func(f library.VideoFile) bool { return knownFiles[f.Path] }) {
			continue
		}
		title, year, id := readName(name, isFile(path))
		u := store.Unmatched{Kind: "movie", Path: path, Name: name, Title: title, Year: year}
		if id == 0 {
			id, u.Note = s.matchMovie(ctx, set, title, year)
		}
		if id == 0 {
			out = append(out, u)
			continue
		}
		if m, ok := s.Movies.Get(id); ok {
			u.Note = fmt.Sprintf("Looks like %s (%d), which is already in the app with another folder.", m.Title, m.Year)
			out = append(out, u)
			continue
		}
		if _, err := s.Movies.AddFound(ctx, id, set.Movies.Quality, path); err != nil {
			u.Note = "Couldn't add it: " + err.Error()
			out = append(out, u)
			continue
		}
		t.movies++
	}
	return out
}

func (s *Service) matchMovie(ctx context.Context, set store.Settings, title string, year int) (int, string) {
	if set.Metadata.TMDBKey == "" {
		return 0, "Add a TMDB key in Settings to look it up."
	}
	text := title
	if year > 0 {
		text += " " + strconv.Itoa(year)
	}
	results, err := metadata.NewTMDB(set.Metadata.TMDBKey).SearchMovies(ctx, text)
	if err != nil {
		return 0, "TMDB: " + err.Error()
	}
	var cands []candidate
	for _, r := range results {
		cands = append(cands, candidate{r.TMDBID, r.Title, r.OriginalTitle, r.Year})
	}
	return pick(title, year, cands)
}

// ---- Shows ----

// newShows adds or lists the folders in the TV folder that no show uses.
func (s *Service) newShows(ctx context.Context, set store.Settings, t *tally) []store.Unmatched {
	d := s.Store.Read()
	known := map[string]bool{}
	for _, sh := range d.Shows {
		known[shows.FolderName(sh)] = true
	}
	var out []store.Unmatched
	for _, path := range s.entries(set.Folders.Shows, d.Library.Skipped) {
		name := filepath.Base(path)
		if isFile(path) || known[name] || len(library.VideoFiles(path)) == 0 {
			continue
		}
		title, year, id := readName(name, false)
		u := store.Unmatched{Kind: "show", Path: path, Name: name, Title: title, Year: year}
		if id == 0 {
			id, u.Note = s.matchShow(ctx, set, title, year)
		}
		if id == 0 {
			out = append(out, u)
			continue
		}
		if sh, ok := s.Shows.Get(id); ok {
			u.Note = fmt.Sprintf("Looks like %s (%d), which is already in the app with another folder.", sh.Title, sh.Year)
			out = append(out, u)
			continue
		}
		if _, err := s.Shows.AddFound(ctx, id, set.Shows.Quality, shows.MonitorNone, name); err != nil {
			u.Note = "Couldn't add it: " + err.Error()
			out = append(out, u)
			continue
		}
		t.shows++
	}
	return out
}

func (s *Service) matchShow(ctx context.Context, set store.Settings, title string, year int) (int, string) {
	if set.Metadata.TMDBKey == "" {
		return 0, "Add a TMDB key in Settings to look it up."
	}
	text := title
	if year > 0 {
		text += " " + strconv.Itoa(year)
	}
	results, err := metadata.NewTMDB(set.Metadata.TMDBKey).SearchShows(ctx, text)
	if err != nil {
		return 0, "TMDB: " + err.Error()
	}
	var cands []candidate
	for _, r := range results {
		cands = append(cands, candidate{r.TMDBID, r.Title, r.OriginalName, r.Year})
	}
	return pick(title, year, cands)
}

// ---- Matching ----

type candidate struct {
	id              int
	title, original string
	year            int
}

// pick returns the one candidate with exactly this title (and year, when
// known), or 0 and why there isn't one.
func pick(title string, year int, cands []candidate) (int, string) {
	if len(cands) == 0 {
		return 0, fmt.Sprintf("TMDB has nothing called %q.", title)
	}
	want := releasename.Normalize(title)
	var exact []candidate
	for _, c := range cands {
		sameTitle := releasename.Normalize(c.title) == want || (c.original != "" && releasename.Normalize(c.original) == want)
		if sameTitle && (year == 0 || c.year == year) {
			exact = append(exact, c)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0].id, ""
	case len(exact) > 1:
		return 0, fmt.Sprintf("TMDB has %d with that exact title; pick the right one.", len(exact))
	default:
		return 0, fmt.Sprintf("No exact title and year match on TMDB (closest: %s%s).", cands[0].title, yearText(cands[0].year))
	}
}

func yearText(y int) string {
	if y == 0 {
		return ""
	}
	return " (" + strconv.Itoa(y) + ")"
}

var (
	reFolderYear = regexp.MustCompile(`^(.+?)\s*\((\d{4})\)`)
	reTMDBTag    = regexp.MustCompile(`(?i)[\[{]tmdb(?:id)?[-=](\d+)[\]}]`)
	reAnyTag     = regexp.MustCompile(`\s*[\[{][^\]}]*[\]}]`)
)

// readName reads a title, year and (when the name carries a {tmdb-123} or
// [tmdbid-123] tag) a TMDB id from a folder or file name:
//
//	"Alien (1979)"                       → Alien, 1979
//	"Alien (1979) {tmdb-348}"            → Alien, 1979, 348
//	"Alien.1979.1080p.BluRay.x264-GRP"   → Alien, 1979
func readName(name string, file bool) (title string, year, tmdbID int) {
	if m := reTMDBTag.FindStringSubmatch(name); m != nil {
		tmdbID, _ = strconv.Atoi(m[1])
	}
	if file {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	clean := strings.TrimSpace(reAnyTag.ReplaceAllString(name, ""))
	if m := reFolderYear.FindStringSubmatch(clean); m != nil {
		year, _ = strconv.Atoi(m[2])
		return strings.TrimSpace(m[1]), year, tmdbID
	}
	if info := releasename.Parse(clean); info.Title != "" {
		return info.Title, info.Year, tmdbID
	}
	return clean, 0, tmdbID
}

// entries lists the folders and files directly in dir, leaving out hidden
// ones and the ones the owner skipped.
func (s *Service) entries(dir string, skipped []string) []string {
	list, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range list {
		path := filepath.Join(dir, e.Name())
		if strings.HasPrefix(e.Name(), ".") || slices.Contains(skipped, path) {
			continue
		}
		out = append(out, path)
	}
	return out
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// ---- Choices from the Library page ----

var errNotListed = errors.New("That folder isn't in the unmatched list any more. Scan again.")

// Match adds the unmatched folder or file at path as the given TMDB movie
// or show.
func (s *Service) Match(ctx context.Context, path string, tmdbID int) (string, error) {
	u, ok := s.unmatched(path)
	if !ok {
		return "", errNotListed
	}
	set := s.Store.Read().Settings
	var title string
	switch u.Kind {
	case "movie":
		m, err := s.Movies.AddFound(ctx, tmdbID, set.Movies.Quality, path)
		if err != nil {
			return "", err
		}
		title = m.Title
	default:
		sh, err := s.Shows.AddFound(ctx, tmdbID, set.Shows.Quality, shows.MonitorNone, u.Name)
		if err != nil {
			return "", err
		}
		title = sh.Title
	}
	s.drop(path, false)
	return title, nil
}

// Skip leaves the folder or file at path alone in later scans.
func (s *Service) Skip(path string) error {
	if _, ok := s.unmatched(path); !ok {
		return errNotListed
	}
	s.drop(path, true)
	return nil
}

// Unskip lets later scans look at path again.
func (s *Service) Unskip(path string) {
	_ = s.Store.Update(func(d *store.Data) error {
		d.Library.Skipped = slices.DeleteFunc(d.Library.Skipped, func(p string) bool { return p == path })
		return nil
	})
}

func (s *Service) unmatched(path string) (store.Unmatched, bool) {
	for _, u := range s.Store.Read().Library.Unmatched {
		if u.Path == path {
			return u, true
		}
	}
	return store.Unmatched{}, false
}

func (s *Service) drop(path string, skip bool) {
	_ = s.Store.Update(func(d *store.Data) error {
		d.Library.Unmatched = slices.DeleteFunc(d.Library.Unmatched, func(u store.Unmatched) bool { return u.Path == path })
		if skip && !slices.Contains(d.Library.Skipped, path) {
			d.Library.Skipped = append(d.Library.Skipped, path)
		}
		return nil
	})
}
