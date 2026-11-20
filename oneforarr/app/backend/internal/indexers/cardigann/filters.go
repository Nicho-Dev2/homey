package cardigann

import (
	"fmt"
	"html"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// applyFilters runs a value through a field's filters, in order. Filter
// arguments are templates too (e.g. "{{ .Config.sitelink }}").
func applyFilters(value string, filters []Filter, data templateData) string {
	for _, f := range filters {
		args := f.ArgList()
		for i := range args {
			args[i] = render(args[i], data)
		}
		value = applyFilter(value, f.Name, args)
	}
	return value
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func applyFilter(v, name string, args []string) string {
	switch name {
	case "replace":
		return strings.ReplaceAll(v, arg(args, 0), arg(args, 1))
	case "re_replace":
		re, err := compileRegexp(arg(args, 0))
		if err != nil {
			logOnce("re_replace pattern %q can't be used here: %v", arg(args, 0), err)
			return v
		}
		return re.ReplaceAllString(v, arg(args, 1))
	case "regexp":
		// The first group of the pattern, or the whole match.
		re, err := compileRegexp(arg(args, 0))
		if err != nil {
			logOnce("regexp pattern %q can't be used here: %v", arg(args, 0), err)
			return v
		}
		m := re.FindStringSubmatch(v)
		switch {
		case m == nil:
			return ""
		case len(m) > 1:
			return m[1]
		default:
			return m[0]
		}
	case "append":
		return v + arg(args, 0)
	case "prepend":
		return arg(args, 0) + v
	case "split":
		parts := strings.Split(v, arg(args, 0))
		i, _ := strconv.Atoi(arg(args, 1))
		if i < 0 {
			i += len(parts)
		}
		if i < 0 || i >= len(parts) {
			return ""
		}
		return parts[i]
	case "trim":
		if cut := arg(args, 0); cut != "" {
			return strings.Trim(v, cut)
		}
		return strings.TrimSpace(v)
	case "tolower":
		return strings.ToLower(v)
	case "toupper":
		return strings.ToUpper(v)
	case "urldecode":
		if d, err := url.QueryUnescape(v); err == nil {
			return d
		}
		return v
	case "urlencode":
		return url.QueryEscape(v)
	case "htmldecode":
		return html.UnescapeString(v)
	case "querystring":
		// The value of one parameter of an address: ?id=123 → 123.
		u, err := url.Parse(v)
		if err != nil {
			return ""
		}
		return u.Query().Get(arg(args, 0))
	case "dateparse":
		// Go layout, e.g. "2006-01-02 15:04". Result: an RFC 3339 time.
		t, err := time.Parse(arg(args, 0), strings.TrimSpace(v))
		if err != nil {
			return v
		}
		return t.Format(time.RFC3339)
	case "timeago", "fuzzytime", "reltime":
		if t, ok := parseRelativeTime(v, time.Now()); ok {
			return t.Format(time.RFC3339)
		}
		return v
	case "diacritics", "validfilename", "strdump", "validate":
		return v
	default:
		logOnce("filter %q isn't supported yet; value left as is", name)
		return v
	}
}

var logged = map[string]bool{}

// logOnce logs a message only the first time, so a missing feature doesn't
// fill the log on every search.
func logOnce(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	templatesMu.Lock()
	defer templatesMu.Unlock()
	if logged[msg] {
		return
	}
	logged[msg] = true
	log.Printf("indexers: %s", msg)
}
