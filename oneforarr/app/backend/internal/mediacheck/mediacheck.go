// Package mediacheck reads how long a video file's picture and sound last,
// to catch files whose sound drifts out of sync: a sound track seconds
// shorter (or longer) than the picture plays fine at first and slips
// further behind as the episode goes on.
//
// It reads only the lengths the file itself records, without decoding: per
// track for Matroska (.mkv, the DURATION tags that mkvmerge and ffmpeg
// write) and MP4 (.mp4, .m4v). Other files, and files without those
// lengths, can't be checked.
package mediacheck

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MaxGap is how far apart the picture and the sound may be before a file
// counts as out of sync. Good files are within a fraction of a second.
const MaxGap = 2 * time.Second

// Lengths is how long a file's picture and its main sound track last.
type Lengths struct {
	Video, Audio time.Duration
}

// Gap is how far apart the picture and the sound are.
func (l Lengths) Gap() time.Duration {
	if l.Video > l.Audio {
		return l.Video - l.Audio
	}
	return l.Audio - l.Video
}

// OutOfSync says whether the sound would drift noticeably.
func (l Lengths) OutOfSync() bool { return l.Gap() > MaxGap }

// Describe says what's wrong, e.g. "its sound is 5.3 s shorter than its
// picture".
func (l Lengths) Describe() string {
	which := "shorter"
	if l.Audio > l.Video {
		which = "longer"
	}
	return fmt.Sprintf("its sound is %.1f s %s than its picture", l.Gap().Seconds(), which)
}

// ErrUnknown: the file doesn't record its tracks' lengths (or isn't a
// kind this package reads), so it can't be checked.
var ErrUnknown = fmt.Errorf("the file doesn't say how long its tracks are")

// Read returns the lengths of a file's video track and its main (default,
// or else first) sound track.
func Read(path string) (Lengths, error) {
	f, err := os.Open(path)
	if err != nil {
		return Lengths{}, err
	}
	defer f.Close()
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mkv", ".webm":
		return readMatroska(f)
	case ".mp4", ".m4v", ".mov":
		return readMP4(f)
	}
	return Lengths{}, ErrUnknown
}

// Problem is a file that's out of sync.
type Problem struct {
	Path    string
	Lengths Lengths
}

func (p Problem) String() string {
	return filepath.Base(p.Path) + ": " + p.Lengths.Describe()
}

// FindProblem checks files and returns the first one out of sync. Files
// that can't be checked pass.
func FindProblem(paths []string) (Problem, bool) {
	for _, p := range paths {
		l, err := Read(p)
		if err == nil && l.OutOfSync() {
			return Problem{Path: p, Lengths: l}, true
		}
	}
	return Problem{}, false
}

// readAt reads exactly len(buf) bytes at off.
func readAt(r io.ReaderAt, off int64, buf []byte) error {
	_, err := r.ReadAt(buf, off)
	return err
}
