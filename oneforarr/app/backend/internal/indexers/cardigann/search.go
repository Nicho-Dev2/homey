package cardigann

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/model"
)

// Site is one added indexer, ready to search: a definition plus the owner's
// settings for it.
type Site struct {
	Def *Definition
	// BaseURL is the site address picked from the definition's links,
	// ending in "/".
	BaseURL string
	// Config is the owner's settings (bool for checkboxes, text otherwise).
	Config map[string]any
	HTTP   *http.Client
	// Pace keeps requests to this site at least Def.RequestDelay apart.
	Pace *Pace
}

// Pace spaces out requests to one site. Share one per site.
type Pace struct {
	mu   sync.Mutex
	last time.Time
}

func (p *Pace) wait(ctx context.Context, gap time.Duration) error {
	if p == nil || gap <= 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if d := time.Until(p.last.Add(gap)); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	p.last = time.Now()
	return nil
}

// userAgent is a normal browser's; some sites refuse unknown ones.
const userAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"

const maxAnswer = 10 << 20 // 10 MiB

// Search asks the site and returns what it found. Results without a title
// or a way to download are left out.
func (s *Site) Search(ctx context.Context, q model.Query) ([]model.Release, error) {
	fields, err := s.Def.FieldList()
	if err != nil {
		return nil, err
	}
	data := s.queryData(q)
	var out []model.Release
	for _, p := range s.pathsFor(q) {
		page, kind, err := s.fetch(ctx, p, data)
		if err != nil {
			return nil, err
		}
		var rows []row
		switch kind {
		case "json":
			rows, err = jsonRows(page, s.Def.Search.Rows)
		default:
			rows, err = htmlRows(page, s.Def.Search.Rows.Selector)
		}
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			rel, ok := s.release(r, fields, data)
			if ok && s.keepRow(rel, data) {
				out = append(out, rel)
			}
		}
	}
	return out, nil
}

// queryData is the template data for one search.
func (s *Site) queryData(q model.Query) templateData {
	mode := map[string]string{"movie": "movie-search", "tv": "tv-search"}[q.Kind]
	if mode == "" {
		mode = "search"
	}
	keywords := strings.TrimSpace(q.Text)
	if q.Kind == "tv" && q.Season > 0 {
		// Few sites take season/episode inputs; the usual way is "Show S01E02".
		tag := fmt.Sprintf("S%02d", q.Season)
		if q.Episode > 0 {
			tag += fmt.Sprintf("E%02d", q.Episode)
		}
		keywords = strings.TrimSpace(keywords + " " + tag)
	}
	keywords = applyFilters(keywords, s.Def.Search.KeywordsFilters, templateData{"Config": s.config()})
	keywords = strings.TrimSpace(keywords)

	imdb := ""
	if q.IMDBID != "" && s.Def.SupportsInput(mode, "imdbid") {
		imdb = q.IMDBID
	}
	query := map[string]any{
		"Keywords":    keywords,
		"Q":           keywords,
		"IMDBID":      imdb,
		"IMDBIDShort": strings.TrimPrefix(imdb, "tt"),
		"Season":      numberOrEmpty(q.Season),
		"Ep":          numberOrEmpty(q.Episode),
		"Year":        "",
	}
	return templateData{
		"Config":     s.config(),
		"Keywords":   keywords,
		"Query":      query,
		"Categories": s.siteCategories(q.Categories),
		"True":       "True",
		"False":      "",
		"Today":      map[string]any{"Year": strconv.Itoa(time.Now().Year())},
		"Result":     map[string]any{},
	}
}

func numberOrEmpty(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// config is the settings as templates see them: defaults, then the owner's
// choices, then sitelink.
func (s *Site) config() map[string]any {
	c := map[string]any{}
	for _, st := range s.Def.Settings {
		c[st.Name] = st.DefaultValue()
	}
	for k, v := range s.Config {
		c[k] = v
	}
	c["sitelink"] = s.BaseURL
	return c
}

// siteCategories turns standard categories (2000) into the site's own ids
// that map to them or to one of their subgroups.
func (s *Site) siteCategories(std []int) []string {
	if len(std) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, m := range s.mappings() {
		for _, want := range std {
			if m.std == want || (want%1000 == 0 && model.CategoryGroup(m.std) == want) {
				if !seen[m.siteID] {
					seen[m.siteID] = true
					out = append(out, m.siteID)
				}
			}
		}
	}
	return out
}

type mapping struct {
	siteID, desc string
	std          int
}

func (s *Site) mappings() []mapping {
	var out []mapping
	for _, m := range s.Def.Caps.CategoryMappings {
		out = append(out, mapping{siteID: m.ID, desc: m.Desc, std: model.CategoryID(m.Cat)})
	}
	for id, name := range s.Def.Caps.Categories {
		out = append(out, mapping{siteID: id, std: model.CategoryID(name)})
	}
	return out
}

// pathsFor picks the search addresses to ask. A path tied to categories is
// only used when the search wants one of them.
func (s *Site) pathsFor(q model.Query) []SearchPath {
	wanted := map[string]bool{}
	for _, c := range s.siteCategories(q.Categories) {
		wanted[c] = true
	}
	var out []SearchPath
	for _, p := range s.Def.Search.Paths {
		if len(p.Categories) > 0 && len(wanted) > 0 {
			match := false
			for _, c := range p.Categories {
				match = match || wanted[c]
			}
			if !match {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

// fetch builds the address for one path, asks the site and returns the
// answer and its kind ("json" or "html").
func (s *Site) fetch(ctx context.Context, p SearchPath, data templateData) ([]byte, string, error) {
	target, err := s.resolve(render(p.Path, data))
	if err != nil {
		return nil, "", err
	}
	inputs := map[string]string{}
	if p.InheritInputs == nil || *p.InheritInputs {
		for k, v := range s.Def.Search.Inputs {
			inputs[k] = v
		}
	}
	for k, v := range p.Inputs {
		inputs[k] = v
	}
	form := url.Values{}
	raw := ""
	for k, v := range inputs {
		val := render(v, data)
		if k == "$raw" {
			raw = val
			continue
		}
		form.Add(k, val)
	}

	method := http.MethodGet
	if strings.EqualFold(p.Method, "post") {
		method = http.MethodPost
	}
	var body io.Reader
	if method == http.MethodGet {
		u, _ := url.Parse(target)
		q := u.Query()
		for k, vs := range form {
			for _, v := range vs {
				q.Add(k, v)
			}
		}
		u.RawQuery = q.Encode()
		if raw != "" {
			if u.RawQuery != "" {
				u.RawQuery += "&"
			}
			u.RawQuery += raw
		}
		target = u.String()
	} else {
		enc := form.Encode()
		if raw != "" {
			enc += "&" + raw
		}
		body = strings.NewReader(enc)
	}

	page, err := s.get(ctx, method, target, body)
	if err != nil {
		return nil, "", err
	}
	kind := strings.ToLower(p.Response.Type)
	if kind == "" {
		kind = "html"
	}
	return page, kind, nil
}

// get sends one request at the site's pace and reads the answer.
func (s *Site) get(ctx context.Context, method, target string, body io.Reader) ([]byte, error) {
	gap := time.Duration(s.Def.RequestDelay * float64(time.Second))
	if err := s.Pace.wait(ctx, gap); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/json;q=0.9,*/*;q=0.8")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, errors.New("The site took too long to answer.")
		}
		return nil, fmt.Errorf("Couldn't reach %s.", req.URL.Host)
	}
	defer resp.Body.Close()
	page, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, fmt.Errorf("Reading the answer from %s failed.", req.URL.Host)
	}
	if resp.StatusCode != http.StatusOK {
		if isCloudflare(resp, page) {
			return nil, errors.New("The site is behind Cloudflare's browser check, which isn't supported yet. Try another site address.")
		}
		return nil, fmt.Errorf("The site answered %d %s.", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	return page, nil
}

func isCloudflare(resp *http.Response, page []byte) bool {
	if resp.StatusCode != 403 && resp.StatusCode != 503 {
		return false
	}
	return strings.HasPrefix(strings.ToLower(resp.Header.Get("Server")), "cloudflare") ||
		strings.Contains(string(page), "challenge-platform")
}

// resolve makes an address absolute, relative to the site address.
func (s *Site) resolve(ref string) (string, error) {
	base, err := url.Parse(s.BaseURL)
	if err != nil {
		return "", fmt.Errorf("bad site address %q", s.BaseURL)
	}
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", fmt.Errorf("bad address %q in the definition", ref)
	}
	return base.ResolveReference(r).String(), nil
}

// release reads one row into a Release. ok is false when a required field
// is missing.
func (s *Site) release(r row, fields []Field, data templateData) (model.Release, bool) {
	result := map[string]any{}
	data["Result"] = result
	var cats []string

	for _, f := range fields {
		name, modifier, _ := strings.Cut(f.Name, "|")
		value, found := s.fieldValue(r, f.Def, data)
		if !found && !f.Def.Optional {
			return model.Release{}, false
		}
		// The default is used when nothing was found, and also when the
		// filters left nothing (e.g. a regexp that didn't match).
		if value == "" && f.Def.Default != nil {
			value = render(*f.Def.Default, data)
		}
		switch {
		case name == "category" || name == "categorydesc":
			if value != "" {
				cats = append(cats, name+"="+value)
			}
			if modifier == "noappend" && len(cats) > 1 {
				cats = cats[len(cats)-1:]
			}
			result[name] = value
		case modifier == "append":
			result[name] = asString(result[name]) + value
		default:
			result[name] = value
		}
	}

	rel := model.Release{
		Title:    strings.TrimSpace(asString(result["title"])),
		Size:     parseSize(asString(result["size"])),
		Seeders:  parseInt(asString(result["seeders"])),
		Leechers: parseInt(asString(result["leechers"])),
		Grabs:    parseInt(asString(result["grabs"])),
		IMDBID:   normalizeIMDB(asString(firstOf(result, "imdbid", "imdb"))),
		InfoHash: strings.ToLower(strings.TrimSpace(asString(result["infohash"]))),
	}
	rel.PublishedAt = parseDate(asString(result["date"]), time.Now())
	rel.DetailsURL, _ = s.resolve(asString(firstOf(result, "details", "comments")))
	if d := strings.TrimSpace(asString(result["download"])); d != "" {
		if strings.HasPrefix(d, "magnet:") {
			rel.MagnetURL = d
		} else {
			rel.DownloadURL, _ = s.resolve(d)
		}
	}
	if m := strings.TrimSpace(asString(result["magnet"])); strings.HasPrefix(m, "magnet:") {
		rel.MagnetURL = m
	}
	if rel.MagnetURL == "" && rel.InfoHash != "" {
		rel.MagnetURL = "magnet:?xt=urn:btih:" + rel.InfoHash + "&dn=" + url.QueryEscape(rel.Title)
	}
	rel.Categories = s.standardCategories(cats)
	if rel.Title == "" || (rel.DownloadURL == "" && rel.MagnetURL == "") {
		return model.Release{}, false
	}
	return rel, true
}

// standardCategories turns the site's category values into standard ids.
func (s *Site) standardCategories(cats []string) []int {
	var out []int
	seen := map[int]bool{}
	for _, c := range cats {
		kind, value, _ := strings.Cut(c, "=")
		for _, m := range s.mappings() {
			hit := (kind == "category" && m.siteID == value) || (kind == "categorydesc" && strings.EqualFold(m.desc, value))
			if hit && m.std != 0 && !seen[m.std] {
				seen[m.std] = true
				out = append(out, m.std)
			}
		}
	}
	return out
}

// fieldValue gets one field: from a template (text:) or by selector, then
// through its filters and case table.
func (s *Site) fieldValue(r row, f FieldDef, data templateData) (string, bool) {
	var value string
	switch {
	case f.Text != nil:
		value = render(*f.Text, data)
	case f.Selector != "" || r.isHTML():
		v, ok := r.selectValue(f)
		if !ok {
			return "", false
		}
		value = v
		if f.Case.Kind != 0 {
			if c, ok := r.caseValue(f, value, data); ok {
				value = c
			}
		}
	default:
		return "", false
	}
	value = applyFilters(value, f.Filters, data)
	return strings.TrimSpace(value), true
}

// keepRow applies the rows filters the app understands (andmatch: every
// search word must be in the title).
func (s *Site) keepRow(rel model.Release, data templateData) bool {
	for _, f := range s.Def.Search.Rows.Filters {
		if f.Name != "andmatch" {
			continue
		}
		kw, _ := data["Keywords"].(string)
		title := strings.ToLower(rel.Title)
		for _, w := range strings.Fields(strings.ToLower(kw)) {
			if !strings.Contains(title, w) {
				return false
			}
		}
	}
	return true
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v := asString(m[k]); v != "" {
			return v
		}
	}
	return ""
}

// ResolveDownload returns the link to give the download client. Some sites
// list only a details page; their definition says where on that page the
// real .torrent or magnet link is.
func (s *Site) ResolveDownload(ctx context.Context, link string) (string, error) {
	if strings.HasPrefix(link, "magnet:") || s.Def.Download == nil || len(s.Def.Download.Selectors) == 0 {
		return link, nil
	}
	// Only fetch pages on the site itself.
	l, err1 := url.Parse(link)
	b, err2 := url.Parse(s.BaseURL)
	if err1 != nil || err2 != nil || !strings.EqualFold(l.Host, b.Host) {
		return "", errors.New("The download link doesn't point to the indexer's site.")
	}
	page, err := s.get(ctx, http.MethodGet, link, nil)
	if err != nil {
		return "", err
	}
	doc, err := parseHTML(page)
	if err != nil {
		return "", err
	}
	// Take the first link, in the definition's order, that qBittorrent can
	// start on its own: a .torrent file, or a magnet that lists trackers.
	// A magnet without trackers can only find peers through DHT, which often
	// never happens for older releases; it's used only if nothing else is
	// on the page.
	data := templateData{"Config": s.config()}
	bareMagnet := ""
	for _, sel := range s.Def.Download.Selectors {
		found, ok := htmlSelect(doc, render(sel.Selector, data), sel.Attribute, "")
		if !ok || found == "" {
			continue
		}
		found = strings.TrimSpace(applyFilters(found, sel.Filters, data))
		switch {
		case found == "":
		case strings.HasPrefix(found, "magnet:") && hasTrackers(found):
			return found, nil
		case strings.HasPrefix(found, "magnet:"):
			if bareMagnet == "" {
				bareMagnet = found
			}
		default:
			return resolveAgainst(link, found)
		}
	}
	if bareMagnet != "" {
		return bareMagnet, nil
	}
	return "", errors.New("The download link wasn't found on the site's page.")
}

// hasTrackers reports whether a magnet link names at least one tracker.
func hasTrackers(magnet string) bool {
	u, err := url.Parse(magnet)
	if err != nil {
		return false
	}
	return len(u.Query()["tr"]) > 0
}

func resolveAgainst(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(strings.TrimSpace(ref))
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}
