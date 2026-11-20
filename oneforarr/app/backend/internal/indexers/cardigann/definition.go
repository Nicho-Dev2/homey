// Package cardigann searches torrent sites by following definition files:
// the YAML format Prowlarr and Jackett use ("Cardigann" definitions). A
// definition says how to build the search address for a site and where in
// its JSON or HTML answer each result's title, size, seeders and links are.
//
// Only part of the format is supported for now: public sites with no
// login, answering in JSON or HTML, in UTF-8. Supported() says why a
// definition can't be used.
package cardigann

import (
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Definition is one site's definition file.
type Definition struct {
	ID          string   `yaml:"id"`
	Name        string   `yaml:"name"`
	Description string   `yaml:"description"`
	Language    string   `yaml:"language"`
	Type        string   `yaml:"type"` // public, semi-private, private
	Encoding    string   `yaml:"encoding"`
	Links       []string `yaml:"links"`
	// RequestDelay is the least time, in seconds, between two requests.
	RequestDelay float64      `yaml:"requestDelay"`
	Caps         Caps         `yaml:"caps"`
	Settings     []Setting    `yaml:"settings"`
	Login        *yaml.Node   `yaml:"login"`
	Download     *DownloadDef `yaml:"download"`
	Search       SearchDef    `yaml:"search"`
}

// Caps says what the site can search and how its categories map to the
// standard ones.
type Caps struct {
	CategoryMappings []CategoryMapping `yaml:"categorymappings"`
	// Categories is an older, simpler form: site id → standard name.
	Categories map[string]string `yaml:"categories"`
	// Modes lists the search kinds ("search", "movie-search", "tv-search")
	// and the inputs each takes (q, imdbid, season, ep).
	Modes map[string][]string `yaml:"modes"`
}

// CategoryMapping ties one of the site's categories to a standard one.
type CategoryMapping struct {
	ID   string `yaml:"id"`
	Cat  string `yaml:"cat"`
	Desc string `yaml:"desc"`
}

// Setting is an option the owner can set for the site (shown on the form).
type Setting struct {
	Name    string    `yaml:"name"`
	Type    string    `yaml:"type"` // text, password, checkbox, select, info...
	Label   string    `yaml:"label"`
	Default yaml.Node `yaml:"default"`
	Options yaml.Node `yaml:"options"` // value → label, in file order
}

// Option is one choice of a select setting.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// OptionList returns the choices of a select setting, in file order.
func (s Setting) OptionList() []Option {
	var out []Option
	n := s.Options
	for i := 0; i+1 < len(n.Content); i += 2 {
		out = append(out, Option{Value: n.Content[i].Value, Label: n.Content[i+1].Value})
	}
	return out
}

// DefaultValue is the setting's default: a bool for checkboxes, else text.
func (s Setting) DefaultValue() any {
	if s.Type == "checkbox" {
		return s.Default.Value == "true"
	}
	return s.Default.Value
}

// DownloadDef says how to find the real download link on a result's page,
// for sites whose search results only link to a details page.
type DownloadDef struct {
	Selectors []struct {
		Selector  string   `yaml:"selector"`
		Attribute string   `yaml:"attribute"`
		Filters   []Filter `yaml:"filters"`
	} `yaml:"selectors"`
	Method string `yaml:"method"`
}

// SearchDef says how to search and how to read the answer.
type SearchDef struct {
	Path            string            `yaml:"path"` // older single-path form
	Paths           []SearchPath      `yaml:"paths"`
	Inputs          map[string]string `yaml:"inputs"`
	KeywordsFilters []Filter          `yaml:"keywordsfilters"`
	Rows            RowsDef           `yaml:"rows"`
	// Fields is kept as a YAML node because order matters: later fields
	// may use earlier ones (.Result.title_default).
	Fields yaml.Node `yaml:"fields"`
}

// SearchPath is one address to ask. Most sites have one.
type SearchPath struct {
	Path          string            `yaml:"path"`
	Method        string            `yaml:"method"`
	Inputs        map[string]string `yaml:"inputs"`
	InheritInputs *bool             `yaml:"inheritinputs"`
	Categories    []string          `yaml:"categories"`
	Response      struct {
		Type string `yaml:"type"` // json or html (default)
	} `yaml:"response"`
}

// RowsDef finds the results in the answer.
type RowsDef struct {
	Selector string `yaml:"selector"`
	// Attribute and Multiple: in JSON, each row holds a list (e.g. a movie
	// with several torrents); every item becomes its own result, and ".."
	// in a field reaches back to the row.
	Attribute                       string    `yaml:"attribute"`
	Multiple                        bool      `yaml:"multiple"`
	MissingAttributeEqualsNoResults bool      `yaml:"missingAttributeEqualsNoResults"`
	Filters                         []Filter  `yaml:"filters"`
	Count                           *FieldDef `yaml:"count"`
}

// FieldDef says how to get one value of a result.
type FieldDef struct {
	Selector  string    `yaml:"selector"`
	Attribute string    `yaml:"attribute"`
	Remove    string    `yaml:"remove"`
	Text      *string   `yaml:"text"`
	Optional  bool      `yaml:"optional"`
	Default   *string   `yaml:"default"`
	Filters   []Filter  `yaml:"filters"`
	Case      yaml.Node `yaml:"case"` // value or selector → result, "*" = otherwise
}

// Filter changes a value: replace, re_replace, regexp, append and so on.
type Filter struct {
	Name string    `yaml:"name"`
	Args yaml.Node `yaml:"args"`
}

// ArgList returns the filter's arguments as text: one, or a list.
func (f Filter) ArgList() []string {
	switch f.Args.Kind {
	case yaml.ScalarNode:
		return []string{f.Args.Value}
	case yaml.SequenceNode:
		out := make([]string, len(f.Args.Content))
		for i, c := range f.Args.Content {
			out[i] = c.Value
		}
		return out
	}
	return nil
}

// Field is one named field, in file order.
type Field struct {
	Name string
	Def  FieldDef
}

// FieldList returns the fields in the order the file lists them.
func (d *Definition) FieldList() ([]Field, error) {
	n := d.Search.Fields
	var out []Field
	for i := 0; i+1 < len(n.Content); i += 2 {
		var fd FieldDef
		if err := n.Content[i+1].Decode(&fd); err != nil {
			return nil, fmt.Errorf("field %s: %w", n.Content[i].Value, err)
		}
		out = append(out, Field{Name: n.Content[i].Value, Def: fd})
	}
	return out, nil
}

// Parse reads a definition file.
func Parse(raw []byte) (*Definition, error) {
	var d Definition
	if err := yaml.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	if d.ID == "" {
		return nil, fmt.Errorf("no id")
	}
	if d.Search.Path != "" && len(d.Search.Paths) == 0 {
		d.Search.Paths = []SearchPath{{Path: d.Search.Path}}
	}
	return &d, nil
}

// secretSettings are settings that mean the site needs an account.
var secretSettings = map[string]bool{
	"username": true, "password": true, "cookie": true, "apikey": true, "passkey": true, "pin": true,
}

// Supported says whether this app can use the definition, and if not, why
// (in words for the owner).
func (d *Definition) Supported() (bool, string) {
	switch {
	case d.Type != "public":
		return false, "Needs an account on the site (not supported yet)."
	case d.Login != nil:
		return false, "Needs a login (not supported yet)."
	case d.Encoding != "" && !strings.EqualFold(d.Encoding, "UTF-8"):
		return false, "Uses " + d.Encoding + " text (only UTF-8 is supported)."
	case len(d.Search.Paths) == 0:
		return false, "The file has no search address."
	}
	for _, s := range d.Settings {
		if secretSettings[strings.ToLower(s.Name)] {
			return false, "Needs an account on the site (not supported yet)."
		}
	}
	for _, p := range d.Search.Paths {
		switch strings.ToLower(p.Response.Type) {
		case "", "html", "json":
		default:
			return false, "Answers in " + p.Response.Type + " (only HTML and JSON are supported)."
		}
	}
	if _, err := d.FieldList(); err != nil {
		return false, "The file couldn't be read: " + err.Error()
	}
	return true, ""
}

// SupportsInput reports whether a search mode ("movie-search") takes an
// input ("imdbid").
func (d *Definition) SupportsInput(mode, input string) bool {
	for _, in := range d.Caps.Modes[mode] {
		if in == input {
			return true
		}
	}
	return false
}
