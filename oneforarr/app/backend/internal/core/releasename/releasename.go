// Package releasename reads what a release name says about itself:
//
//	Night.of.the.Living.Dead.1968.1080p.BluRay.x264-GROUP
//	→ title "Night of the Living Dead", year 1968, 1080p, Blu-ray, x264
//
// Names follow loose habits, not rules, so this reads the common ones and
// says "unknown" for the rest.
package releasename

import (
	"regexp"
	"strconv"
	"strings"
)

// Source is where the video came from, best last.
type Source int

const (
	SourceUnknown  Source = iota
	SourceCam             // filmed in a cinema: never wanted
	SourceTelesync        // cinema copy with better sound: never wanted
	SourceTelecine
	SourceScreener
	SourceDVD
	SourceHDTV
	SourceWEBRip // recorded from a streaming service
	SourceWEBDL  // downloaded as is from a streaming service
	SourceBluray
	SourceRemux // Blu-ray video untouched: best, and very large
)

var sourceNames = map[Source]string{
	SourceUnknown: "Unknown", SourceCam: "CAM", SourceTelesync: "TELESYNC", SourceTelecine: "TELECINE",
	SourceScreener: "SCREENER", SourceDVD: "DVD", SourceHDTV: "HDTV", SourceWEBRip: "WEBRip",
	SourceWEBDL: "WEBDL", SourceBluray: "Bluray", SourceRemux: "Remux",
}

func (s Source) String() string { return sourceNames[s] }

// Cinema says whether the source is a cinema recording or a pre-release
// copy, which is never wanted.
func (s Source) Cinema() bool {
	return s == SourceCam || s == SourceTelesync || s == SourceTelecine || s == SourceScreener
}

// Info is what a release name says.
type Info struct {
	Title      string `json:"title"`
	Year       int    `json:"year,omitempty"`
	Resolution int    `json:"resolution,omitempty"` // 480, 576, 720, 1080, 2160; 0 unknown
	Source     Source `json:"-"`
	SourceName string `json:"source"`
	Codec      string `json:"codec,omitempty"`  // x264, x265, AV1...
	Proper     bool   `json:"proper,omitempty"` // a PROPER or REPACK: a fixed release
	// Fix counts fixes: 1 for PROPER or REPACK, 2 for REPACK2, and so on.
	// Repack: the fix is by the group that made the first release (a
	// PROPER is by another group).
	Fix    int    `json:"fix,omitempty"`
	Repack bool   `json:"repack,omitempty"`
	Group  string `json:"group,omitempty"` // the release group: "x264-GRP" → "GRP"
	ThreeD bool   `json:"threeD,omitempty"`
	// Season and Episode are set for TV names like "Show S01E02".
	Season  int `json:"season,omitempty"`
	Episode int `json:"episode,omitempty"`
}

// Quality is the short name used in file names: "Bluray-1080p".
func (i Info) Quality() string {
	res := "Unknown"
	if i.Resolution > 0 {
		res = strconv.Itoa(i.Resolution) + "p"
	}
	if i.Source == SourceUnknown {
		return res
	}
	return i.Source.String() + "-" + res
}

var (
	reSplit   = regexp.MustCompile(`[\s._\[\]()]+`)
	reYear    = regexp.MustCompile(`^(19|20)\d\d$`)
	reEpisode = regexp.MustCompile(`(?i)^s(\d{1,2})(?:e(\d{1,3}))?$`)
	reRes     = regexp.MustCompile(`(?i)^(480|576|720|1080|2160)[pi]$`)
	reCodec   = regexp.MustCompile(`(?i)^(x264|x265|h264|h265|hevc|avc|av1|xvid|vc-?1)$`)
	reFix     = regexp.MustCompile(`^(proper|repack|rerip)(\d?)$`)
	reTrailer = regexp.MustCompile(`\s*\[[^\]]*\]\s*$`) // "[eztv]", "[rarbg]"
)

// Words that mark the end of the title and what they mean.
var sourceWords = map[string]Source{
	"cam": SourceCam, "camrip": SourceCam, "hdcam": SourceCam,
	"ts": SourceTelesync, "telesync": SourceTelesync, "hdts": SourceTelesync, "pdvd": SourceTelesync, "tsrip": SourceTelesync,
	"tc": SourceTelecine, "telecine": SourceTelecine, "hdtc": SourceTelecine,
	"scr": SourceScreener, "screener": SourceScreener, "dvdscr": SourceScreener, "bdscr": SourceScreener,
	"dvd": SourceDVD, "dvdrip": SourceDVD, "dvd5": SourceDVD, "dvd9": SourceDVD, "dvdr": SourceDVD,
	"hdtv": SourceHDTV, "pdtv": SourceHDTV, "hdtvrip": SourceHDTV,
	"webrip": SourceWEBRip, "web-rip": SourceWEBRip,
	"web": SourceWEBDL, "web-dl": SourceWEBDL, "webdl": SourceWEBDL,
	"bluray": SourceBluray, "blu-ray": SourceBluray, "bdrip": SourceBluray, "brrip": SourceBluray,
	"bd": SourceBluray, "bdremux": SourceRemux, "remux": SourceRemux,
}

var resWords = map[string]int{"4k": 2160, "uhd": 2160, "sd": 480}

// Parse reads a release name.
func Parse(name string) Info {
	tokens := tokenize(name)
	var info Info
	// The title ends at the first "strong" marker: year, SxxEyy,
	// resolution or codec. Source words ("web", "ts") also appear in titles
	// ("Charlotte's Web"), so they only end the title when no strong
	// marker exists.
	strongEnd, weakEnd := len(tokens), len(tokens)
	for i, tok := range tokens {
		low := strings.ToLower(tok)
		strong, weak := false, false
		switch {
		case i > 0 && reYear.MatchString(tok) && info.Year == 0:
			// The last year-like word before the tags is the year;
			// earlier ones belong to the title ("Blade Runner 2049 2017").
			if !moreYearsBeforeTags(tokens[i+1:]) {
				info.Year, _ = strconv.Atoi(tok)
				strong = true
			}
		case reEpisode.MatchString(tok):
			m := reEpisode.FindStringSubmatch(tok)
			info.Season, _ = strconv.Atoi(m[1])
			if m[2] != "" {
				info.Episode, _ = strconv.Atoi(m[2])
			}
			strong = true
		case reRes.MatchString(tok):
			info.Resolution, _ = strconv.Atoi(tok[:len(tok)-1])
			strong = true
		case reCodec.MatchString(tok):
			info.Codec = strings.ToLower(tok)
			strong = true
		case resWords[low] != 0 && i > 0:
			if info.Resolution == 0 {
				info.Resolution = resWords[low]
			}
			weak = true
		case sourceWords[low] != SourceUnknown && i > 0:
			weak = true
		case reFix.MatchString(low):
			m := reFix.FindStringSubmatch(low)
			fix := 1
			if m[2] != "" {
				fix, _ = strconv.Atoi(m[2])
			}
			info.Fix = max(info.Fix, fix)
			info.Proper = true
			info.Repack = info.Repack || m[1] != "proper"
			weak = true
		case low == "3d":
			info.ThreeD = true
			weak = true
		}
		if strong && i < strongEnd {
			strongEnd = i
		}
		if weak && i < weakEnd {
			weakEnd = i
		}
	}
	titleEnd := strongEnd
	if strongEnd == len(tokens) {
		titleEnd = weakEnd
	}
	// Sources count only after the title. Remux beats Blu-ray; otherwise
	// the first source named counts.
	for _, tok := range tokens[titleEnd:] {
		if s := sourceWords[strings.ToLower(tok)]; s != SourceUnknown {
			if info.Source == SourceUnknown || s == SourceRemux {
				info.Source = s
			}
		}
	}
	info.Title = strings.Join(tokens[:titleEnd], " ")
	info.Group = group(name)
	info.SourceName = info.Source.String()
	// A Blu-ray without a resolution is usually 1080p; a DVD is SD.
	if info.Resolution == 0 && info.Source == SourceBluray {
		info.Resolution = 1080
	}
	if info.Resolution == 0 && info.Source == SourceDVD {
		info.Resolution = 480
	}
	return info
}

// tokenize splits on spaces, dots, underscores and brackets, and drops a
// trailing "-GROUP" from the last word.
// group reads the release group: the part after the last "-", as in
// "Show.S01E02.1080p.WEB.h264-GRP" or "… H 264-GRP [eztv]".
func group(name string) string {
	name = strings.TrimSpace(name)
	for _, ext := range []string{".mkv", ".mp4", ".avi", ".m4v", ".torrent"} {
		name = strings.TrimSuffix(name, ext)
	}
	for reTrailer.MatchString(name) {
		name = reTrailer.ReplaceAllString(name, "")
	}
	i := strings.LastIndex(name, "-")
	if i < 0 {
		return ""
	}
	g := name[i+1:]
	switch strings.ToLower(g) {
	case "", "dl", "rip", "ray": // "WEB-DL", "Blu-ray" at the very end
		return ""
	}
	if strings.ContainsAny(g, " ._") {
		return ""
	}
	return g
}

func tokenize(name string) []string {
	name = strings.TrimSpace(name)
	for _, ext := range []string{".mkv", ".mp4", ".avi", ".m4v", ".torrent"} {
		name = strings.TrimSuffix(name, ext)
	}
	var out []string
	for _, t := range reSplit.Split(name, -1) {
		if t == "" || t == "-" {
			continue
		}
		// "x264-GROUP" → "x264"; but keep "web-dl", "blu-ray", "Spider-Man".
		if i := strings.LastIndex(t, "-"); i > 0 && reCodec.MatchString(t[:i]) {
			t = t[:i]
		}
		out = append(out, t)
	}
	return out
}

// moreYearsBeforeTags reports whether another year comes before the first
// quality word: then the current one is part of the title.
func moreYearsBeforeTags(rest []string) bool {
	for _, t := range rest {
		low := strings.ToLower(t)
		if reRes.MatchString(t) || sourceWords[low] != SourceUnknown || resWords[low] != 0 || reCodec.MatchString(t) {
			return false
		}
		if reYear.MatchString(t) {
			return true
		}
	}
	return false
}

var (
	reNonWord = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	reAnd     = regexp.MustCompile(`(?i)\s&\s`)
)

// Normalize makes a title comparable: lower case, "&" as "and", no
// punctuation or apostrophes, single spaces.
// "Spider-Man: Into the Spider-Verse" → "spider man into the spider verse".
func Normalize(title string) string {
	t := reAnd.ReplaceAllString(" "+title+" ", " and ")
	t = strings.NewReplacer("'", "", "’", "", "`", "").Replace(t)
	t = reNonWord.ReplaceAllString(strings.ToLower(t), " ")
	return strings.TrimSpace(t)
}
