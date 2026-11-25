package mediacheck

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The test files are 8 seconds of picture, made with ffmpeg; the "drift"
// ones have only 3 seconds of sound. two-audio.mkv has a short first
// sound track and a full one marked as the default.
func TestRead(t *testing.T) {
	for _, c := range []struct {
		file       string
		outOfSync  bool
		videoAbout time.Duration
	}{
		{"good.mkv", false, 8 * time.Second},
		{"drift.mkv", true, 8 * time.Second},
		{"good.mp4", false, 8 * time.Second},
		{"drift.mp4", true, 8 * time.Second}, // moov at the start (faststart)
		{"two-audio.mkv", false, 8 * time.Second},
	} {
		l, err := Read(filepath.Join("testdata", c.file))
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}
		if l.OutOfSync() != c.outOfSync || l.Video != c.videoAbout {
			t.Errorf("%s: %+v (gap %v)", c.file, l, l.Gap())
		}
	}
}

func TestDescribe(t *testing.T) {
	l := Lengths{Video: 51*time.Minute + 9233*time.Millisecond, Audio: 51*time.Minute + 3936*time.Millisecond}
	if got := l.Describe(); got != "its sound is 5.3 s shorter than its picture" {
		t.Errorf("got %q", got)
	}
}

func TestUncheckable(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{"x.avi": "RIFF", "x.mkv": "not matroska", "x.mp4": "\x00\x00\x00\x08free"} {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(data), 0o644)
		if _, err := Read(p); !errors.Is(err, ErrUnknown) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Files that can't be checked pass.
	if _, bad := FindProblem([]string{filepath.Join(dir, "x.avi"), filepath.Join("testdata", "good.mkv")}); bad {
		t.Error("a problem in good files")
	}
	if p, bad := FindProblem([]string{filepath.Join("testdata", "good.mp4"), filepath.Join("testdata", "drift.mkv")}); !bad || filepath.Base(p.Path) != "drift.mkv" {
		t.Errorf("got %v %v", p, bad)
	}
}
