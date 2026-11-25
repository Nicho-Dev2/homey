package releasename

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		name            string
		title           string
		year, res       int
		source          Source
		season, episode int
	}{
		{"Night.of.the.Living.Dead.1968.1080p.BluRay.x264-GROUP", "Night of the Living Dead", 1968, 1080, SourceBluray, 0, 0},
		{"Night of the Living Dead (1968) 1080p BRRip x264 -YTS", "Night of the Living Dead", 1968, 1080, SourceBluray, 0, 0},
		{"Night of the Living Dead 1968 1080p BluRay x264", "Night of the Living Dead", 1968, 1080, SourceBluray, 0, 0},
		{"Dune.Part.Two.2024.2160p.UHD.BluRay.REMUX.HDR.HEVC.Atmos-GRP", "Dune Part Two", 2024, 2160, SourceRemux, 0, 0},
		{"Some.Movie.2023.HDCAM.x264", "Some Movie", 2023, 0, SourceCam, 0, 0},
		{"Some Movie 2023 HDTS 720p", "Some Movie", 2023, 720, SourceTelesync, 0, 0},
		{"Blade.Runner.2049.2017.1080p.WEB-DL.DD5.1.H264", "Blade Runner 2049", 2017, 1080, SourceWEBDL, 0, 0},
		{"2001.A.Space.Odyssey.1968.REMASTERED.720p.BluRay", "2001 A Space Odyssey", 1968, 720, SourceBluray, 0, 0},
		{"1917.2019.1080p.WEBRip.x265", "1917", 2019, 1080, SourceWEBRip, 0, 0},
		{"Charlotte's.Web.2006.720p.BluRay.x264", "Charlotte's Web", 2006, 720, SourceBluray, 0, 0},
		{"Some.Show.S01E02.1080p.WEB.h264-GRP", "Some Show", 0, 1080, SourceWEBDL, 1, 2},
		{"Some Show S02 Complete 720p HDTV", "Some Show", 0, 720, SourceHDTV, 2, 0},
		{"Old.Film.1950.DVDRip.XviD", "Old Film", 1950, 480, SourceDVD, 0, 0},
		{"Night of the Living Dead", "Night of the Living Dead", 0, 0, SourceUnknown, 0, 0},
		{"Movie.Title.2020.4K.WEB", "Movie Title", 2020, 2160, SourceWEBDL, 0, 0},
	}
	for _, c := range cases {
		got := Parse(c.name)
		if got.Title != c.title || got.Year != c.year || got.Resolution != c.res || got.Source != c.source ||
			got.Season != c.season || got.Episode != c.episode {
			t.Errorf("Parse(%q) = %+v", c.name, got)
		}
	}
}

func TestQuality(t *testing.T) {
	if q := Parse("A.2020.1080p.BluRay").Quality(); q != "Bluray-1080p" {
		t.Errorf("quality %q", q)
	}
	if q := Parse("A 2020").Quality(); q != "Unknown" {
		t.Errorf("quality %q", q)
	}
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"Spider-Man: Into the Spider-Verse": "spider man into the spider verse",
		"Charlotte's Web":                   "charlottes web",
		"Fast & Furious":                    "fast and furious",
		"  Amélie ":                         "amélie",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGroupAndFix(t *testing.T) {
	for _, c := range []struct {
		name   string
		group  string
		fix    int
		repack bool
	}{
		{"Good Daughter 2026 S01E02 1080p AMZN WEB-DL DDP5 1 H 264-RAWR", "RAWR", 0, false},
		{"good daughter 2026 s01e04 1080p web h264-insidious", "insidious", 0, false},
		{"Good.Daughter.2026.S01E04.REPACK.1080p.WEB.h264-INSIDIOUS[eztv].mkv", "INSIDIOUS", 1, true},
		{"Show.S01E04.REPACK2.1080p.WEB.h264-GRP", "GRP", 2, true},
		{"Show.S01E04.PROPER.1080p.WEB.h264-OTHER", "OTHER", 1, false},
		{"Movie 2020 1080p WEB-DL", "", 0, false},
		{"Spider-Man 2002 1080p BluRay x264", "", 0, false},
	} {
		i := Parse(c.name)
		if i.Group != c.group || i.Fix != c.fix || i.Repack != c.repack || i.Proper != (c.fix > 0) {
			t.Errorf("%s: group %q fix %d repack %v", c.name, i.Group, i.Fix, i.Repack)
		}
	}
}
