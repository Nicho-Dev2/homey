package cardigann

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// row is one result in the answer: an HTML element or a JSON object (with
// its parent, for definitions that list several torrents per item).
type row struct {
	node   *html.Node
	obj    any
	parent any
}

func (r row) isHTML() bool { return r.node != nil }

// ---- JSON ----

// jsonRows finds the results in a JSON answer.
func jsonRows(page []byte, def RowsDef) ([]row, error) {
	dec := json.NewDecoder(bytes.NewReader(page))
	dec.UseNumber() // keep big numbers (sizes in bytes) exact
	var root any
	if err := dec.Decode(&root); err != nil {
		return nil, errors.New("The site's answer wasn't valid JSON. The site may have changed.")
	}
	found := jsonPath(root, def.Selector)
	var items []any
	switch v := found.(type) {
	case []any:
		items = v
	case map[string]any:
		// Some sites list results as an object keyed by id.
		for _, item := range v {
			items = append(items, item)
		}
	case nil:
		return nil, nil // no results
	default:
		return nil, fmt.Errorf("The results at %q aren't a list.", def.Selector)
	}

	var rows []row
	for _, item := range items {
		if def.Attribute == "" {
			rows = append(rows, row{obj: item})
			continue
		}
		inner := jsonPath(item, def.Attribute)
		switch v := inner.(type) {
		case []any:
			for _, sub := range v {
				rows = append(rows, row{obj: sub, parent: item})
			}
		case nil:
			// No torrents for this item; MissingAttributeEqualsNoResults
			// says this is normal for the site, and it is for us either way.
		default:
			rows = append(rows, row{obj: v, parent: item})
		}
	}
	return rows, nil
}

// jsonPath follows a dotted path: "data.movies", "files.0.name".
func jsonPath(v any, path string) any {
	if path == "" || path == "$" {
		return v
	}
	for _, key := range strings.Split(path, ".") {
		switch cur := v.(type) {
		case map[string]any:
			v = cur[key]
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(cur) {
				return nil
			}
			v = cur[i]
		default:
			return nil
		}
	}
	return v
}

func jsonText(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "", false
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		if x {
			return "True", true
		}
		return "False", true
	case []any:
		parts := make([]string, 0, len(x))
		for _, p := range x {
			if s, ok := jsonText(p); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ","), true
	default:
		return "", false
	}
}

// ---- HTML ----

func parseHTML(page []byte) (*html.Node, error) {
	doc, err := html.Parse(bytes.NewReader(page))
	if err != nil {
		return nil, errors.New("The site's page couldn't be read.")
	}
	return doc, nil
}

// htmlRows finds the results in an HTML page with a CSS selector.
func htmlRows(page []byte, selector string) ([]row, error) {
	doc, err := parseHTML(page)
	if err != nil {
		return nil, err
	}
	sel, err := compileSelector(selector)
	if err != nil {
		return nil, fmt.Errorf("The definition's row selector %q isn't supported: %v", selector, err)
	}
	var rows []row
	for _, n := range cascadia.QueryAll(doc, sel) {
		rows = append(rows, row{node: n})
	}
	return rows, nil
}

func compileSelector(s string) (cascadia.Sel, error) {
	return cascadia.Parse(strings.TrimSpace(s))
}

// htmlSelect finds the first element matching selector under n (or n itself
// when selector is empty) and returns an attribute or its text.
func htmlSelect(n *html.Node, selector, attribute, remove string) (string, bool) {
	el := n
	if strings.TrimSpace(selector) != "" {
		sel, err := compileSelector(selector)
		if err != nil {
			logOnce("selector %q isn't supported: %v", selector, err)
			return "", false
		}
		el = cascadia.Query(n, sel)
		if el == nil {
			return "", false
		}
	}
	if attribute != "" {
		for _, a := range el.Attr {
			if a.Key == attribute {
				return a.Val, true
			}
		}
		return "", false
	}
	return textOf(el, remove), true
}

// textOf is an element's visible text, spaces tidied, leaving out any
// parts that match remove.
func textOf(n *html.Node, remove string) string {
	var skip map[*html.Node]bool
	if remove != "" {
		if sel, err := compileSelector(remove); err == nil {
			skip = map[*html.Node]bool{}
			for _, m := range cascadia.QueryAll(n, sel) {
				skip[m] = true
			}
		}
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(c *html.Node) {
		if skip[c] {
			return
		}
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
			b.WriteByte(' ')
		}
		for k := c.FirstChild; k != nil; k = k.NextSibling {
			walk(k)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

// ---- both ----

// selectValue gets a field's raw value from the row.
func (r row) selectValue(f FieldDef) (string, bool) {
	if r.isHTML() {
		return htmlSelect(r.node, f.Selector, f.Attribute, f.Remove)
	}
	src, path := r.obj, f.Selector
	if strings.HasPrefix(path, "..") {
		src, path = r.parent, strings.TrimPrefix(path, "..")
	}
	return jsonText(jsonPath(src, path))
}

// caseValue maps a value through the field's case table. In HTML the keys
// are selectors (does the element contain a match?); in JSON they are
// values. "*" is the fallback.
func (r row) caseValue(f FieldDef, value string, data templateData) (string, bool) {
	n := f.Case
	fallback, hasFallback := "", false
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, out := n.Content[i].Value, n.Content[i+1].Value
		if key == "*" {
			fallback, hasFallback = out, true
			continue
		}
		if r.isHTML() {
			el := r.node
			if f.Selector != "" {
				if sel, err := compileSelector(f.Selector); err == nil {
					if m := cascadia.Query(r.node, sel); m != nil {
						el = m
					}
				}
			}
			if sel, err := compileSelector(key); err == nil && (sel.Match(el) || cascadia.Query(el, sel) != nil) {
				return render(out, data), true
			}
			continue
		}
		if key == value {
			return render(out, data), true
		}
	}
	if hasFallback {
		return render(fallback, data), true
	}
	return "", false
}
