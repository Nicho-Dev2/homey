package library

import "testing"

func TestFixesRelease(t *testing.T) {
	const have = "Good.Daughter.2026.S01E04.1080p.WEB.h264-INSIDIOUS"
	for _, c := range []struct {
		title, file string
		want        bool
	}{
		{"Good.Daughter.2026.S01E04.REPACK.1080p.WEB.h264-INSIDIOUS", have, true},
		{"good daughter 2026 s01e04 repack 1080p web h264-insidious", have, true}, // as an indexer shows it
		{"Good.Daughter.2026.S01E04.REPACK.1080p.WEB.h264-OTHER", have, false},    // a REPACK by another group
		{"Good.Daughter.2026.S01E04.PROPER.1080p.WEB.h264-OTHER", have, true},     // a PROPER may be anyone's
		{"Good.Daughter.2026.S01E05.REPACK.1080p.WEB.h264-INSIDIOUS", have, false},
		{"Good.Daughter.2026.S01E04.REPACK.720p.WEB.h264-INSIDIOUS", have, false},
		{"Good.Daughter.2026.S01E04.1080p.WEB.h264-INSIDIOUS", have, false}, // not a fix
		{"Good.Daughter.2026.S01E04.REPACK.1080p.WEB.h264-INSIDIOUS", "", false},
		{"Good.Daughter.2026.S01E04.REPACK2.1080p.WEB.h264-INSIDIOUS", "Good.Daughter.2026.S01E04.REPACK.1080p.WEB.h264-INSIDIOUS", true},
		{"Good.Daughter.2026.S01E04.REPACK.1080p.WEB.h264-INSIDIOUS", "Good.Daughter.2026.S01E04.REPACK.1080p.WEB.h264-INSIDIOUS", false},
		{"Show.S01.REPACK.1080p.WEB.h264-GRP", "Show.S01.1080p.WEB.h264-GRP", true},      // a fixed season pack
		{"Show.S01E03.PROPER.1080p.WEB.h264-OTHER", "Show.S01.1080p.WEB.h264-GRP", true}, // one episode of a pack
		{"Movie.2020.REPACK.1080p.BluRay.x264-GRP", "Movie.2020.1080p.BluRay.x264-GRP", true},
		{"Movie.2020.REPACK.1080p.WEBRip.x264-GRP", "Movie.2020.1080p.BluRay.x264-GRP", false}, // a worse source
	} {
		if got := FixesRelease(c.title, c.file); got != c.want {
			t.Errorf("%s fixes %q: got %v", c.title, c.file, got)
		}
	}
}
