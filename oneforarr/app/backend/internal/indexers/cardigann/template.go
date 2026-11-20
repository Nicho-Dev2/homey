package cardigann

import (
	"regexp"
	"strings"
	"sync"
	"text/template"
)

// Definitions write values as Go-style templates:
//
//	{{ if .Keywords }}search/{{ .Keywords }}{{ else }}latest{{ end }}
//
// with a few extra functions. The data has:
//
//	.Config      the site's settings, plus .Config.sitelink (the site address)
//	.Keywords    the search words
//	.Query       .Keywords .IMDBID .IMDBIDShort .Season .Ep .Year
//	.Categories  the site's own category ids for this search
//	.Result      the fields of the current result found so far
//	.True .False .Today.Year
type templateData map[string]any

var templateFuncs = template.FuncMap{
	"join": func(list any, sep string) string {
		switch v := list.(type) {
		case []string:
			return strings.Join(v, sep)
		case string:
			return v
		}
		return ""
	},
	"re_replace": func(s, pattern, repl string) string {
		re, err := compileRegexp(pattern)
		if err != nil {
			return s
		}
		return re.ReplaceAllString(s, repl)
	},
}

var (
	templatesMu sync.Mutex
	templates   = map[string]*template.Template{}
)

// render fills in a template string. Text without "{{" is returned as is.
// A broken template gives back the text unchanged, as Prowlarr does.
func render(text string, data templateData) string {
	if !strings.Contains(text, "{{") {
		return text
	}
	templatesMu.Lock()
	t, ok := templates[text]
	if !ok {
		var err error
		t, err = template.New("").Funcs(templateFuncs).Option("missingkey=zero").Parse(text)
		if err != nil {
			t = nil
		}
		templates[text] = t
	}
	templatesMu.Unlock()
	if t == nil {
		return text
	}
	var b strings.Builder
	if err := t.Execute(&b, map[string]any(data)); err != nil {
		return text
	}
	// A missing value prints as "<no value>"; definitions expect nothing.
	return strings.ReplaceAll(b.String(), "<no value>", "")
}

var (
	regexpsMu sync.Mutex
	regexps   = map[string]*regexp.Regexp{}
	regexpErr = map[string]error{}
)

// compileRegexp compiles and keeps a pattern. Definitions are written for
// .NET regular expressions; Go's are nearly the same, but a few features
// (look-behind, back-references) aren't available, and those patterns fail.
func compileRegexp(pattern string) (*regexp.Regexp, error) {
	regexpsMu.Lock()
	defer regexpsMu.Unlock()
	if re, ok := regexps[pattern]; ok {
		return re, nil
	}
	if err, ok := regexpErr[pattern]; ok {
		return nil, err
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		regexpErr[pattern] = err
		return nil, err
	}
	regexps[pattern] = re
	return re, nil
}
