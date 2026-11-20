package cardigann

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Library is the folder of definition files. Files are read when needed
// and kept until they change on disk.
type Library struct {
	Dir string

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	mod time.Time
	def *Definition
	err error
}

// Summary is what the Add Indexer list shows for each file.
type Summary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Language    string `json:"language"`
	Type        string `json:"type"`
	Supported   bool   `json:"supported"`
	Reason      string `json:"reason,omitempty"`
}

var validID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// ErrNoDefinition means there is no file for that id.
var ErrNoDefinition = errors.New("no definition file")

// Get returns the definition with this id (the file <id>.yml).
func (l *Library) Get(id string) (*Definition, error) {
	if !validID.MatchString(id) {
		return nil, ErrNoDefinition
	}
	return l.load(filepath.Join(l.Dir, id+".yml"))
}

func (l *Library) load(path string) (*Definition, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoDefinition
	}
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	if c, ok := l.cache[path]; ok && c.mod.Equal(info.ModTime()) {
		l.mu.Unlock()
		return c.def, c.err
	}
	l.mu.Unlock()

	raw, err := os.ReadFile(path)
	var def *Definition
	if err == nil {
		def, err = Parse(raw)
	}
	if err != nil {
		err = fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	l.mu.Lock()
	if l.cache == nil {
		l.cache = map[string]cached{}
	}
	l.cache[path] = cached{mod: info.ModTime(), def: def, err: err}
	l.mu.Unlock()
	return def, err
}

// List reads every definition in the folder, sorted by name. A missing
// folder is an empty list.
func (l *Library) List() ([]Summary, error) {
	files, err := filepath.Glob(filepath.Join(l.Dir, "*.yml"))
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(files))
	for _, f := range files {
		d, err := l.load(f)
		if err != nil {
			id := strings.TrimSuffix(filepath.Base(f), ".yml")
			out = append(out, Summary{ID: id, Name: id, Reason: "The file couldn't be read: " + err.Error()})
			continue
		}
		ok, why := d.Supported()
		out = append(out, Summary{
			ID: d.ID, Name: d.Name, Description: d.Description, Language: d.Language,
			Type: d.Type, Supported: ok, Reason: why,
		})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}
