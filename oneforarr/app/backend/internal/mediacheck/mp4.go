package mediacheck

import (
	"encoding/binary"
	"os"
	"time"
)

// MP4 is a tree of boxes, each a size and a four-letter type. Each track
// (moov → trak) has its kind in mdia → hdlr ("vide" or "soun") and its
// length in mdia → mdhd (a duration in its own time scale). The moov box
// can be at the start or the end of the file.
func readMP4(f *os.File) (Lengths, error) {
	st, err := f.Stat()
	if err != nil {
		return Lengths{}, err
	}
	moov, ok := findBox(f, 0, st.Size(), "moov")
	if !ok || moov.end-moov.data > maxElement {
		return Lengths{}, ErrUnknown
	}
	body := make([]byte, moov.end-moov.data)
	if err := readAt(f, moov.data, body); err != nil {
		return Lengths{}, ErrUnknown
	}
	var l Lengths
	for _, trak := range boxes(body) {
		if trak.kind != "trak" {
			continue
		}
		mdia, ok := boxIn(trak.body, "mdia")
		if !ok {
			continue
		}
		hdlr, ok1 := boxIn(mdia, "hdlr")
		mdhd, ok2 := boxIn(mdia, "mdhd")
		if !ok1 || !ok2 || len(hdlr) < 12 {
			continue
		}
		length := mdhdLength(mdhd)
		switch string(hdlr[8:12]) {
		case "vide":
			if l.Video == 0 {
				l.Video = length
			}
		case "soun":
			if l.Audio == 0 {
				l.Audio = length // the first sound track is the main one
			}
		}
	}
	if l.Video == 0 || l.Audio == 0 {
		return Lengths{}, ErrUnknown
	}
	return l, nil
}

type boxSpan struct{ data, end int64 }

// findBox finds a top-level box by walking the file's boxes.
func findBox(f *os.File, off, end int64, kind string) (boxSpan, bool) {
	for off+8 <= end {
		var head [16]byte
		if err := readAt(f, off, head[:8]); err != nil {
			return boxSpan{}, false
		}
		size := int64(binary.BigEndian.Uint32(head[:4]))
		data := off + 8
		switch size {
		case 0: // runs to the end of the file
			size = end - off
		case 1: // a 64-bit size follows
			if err := readAt(f, off+8, head[8:16]); err != nil {
				return boxSpan{}, false
			}
			size, data = int64(binary.BigEndian.Uint64(head[8:16])), off+16
		}
		if size < data-off || off+size > end {
			return boxSpan{}, false
		}
		if string(head[4:8]) == kind {
			return boxSpan{data: data, end: off + size}, true
		}
		off += size
	}
	return boxSpan{}, false
}

type box struct {
	kind string
	body []byte
}

// boxes splits a box's contents (in memory) into its boxes.
func boxes(b []byte) []box {
	var out []box
	for len(b) >= 8 {
		size := int(binary.BigEndian.Uint32(b[:4]))
		head := 8
		if size == 1 && len(b) >= 16 {
			size, head = int(binary.BigEndian.Uint64(b[8:16])), 16
		} else if size == 0 {
			size = len(b)
		}
		if size < head || size > len(b) {
			break
		}
		out = append(out, box{kind: string(b[4:8]), body: b[head:size]})
		b = b[size:]
	}
	return out
}

func boxIn(b []byte, kind string) ([]byte, bool) {
	for _, x := range boxes(b) {
		if x.kind == kind {
			return x.body, true
		}
	}
	return nil, false
}

// mdhdLength reads a track's length from its mdhd box: version 0 has
// 32-bit times, version 1 64-bit.
func mdhdLength(b []byte) time.Duration {
	var scale, units uint64
	switch {
	case len(b) >= 24 && b[0] == 0:
		scale, units = uint64(binary.BigEndian.Uint32(b[12:16])), uint64(binary.BigEndian.Uint32(b[16:20]))
	case len(b) >= 36 && b[0] == 1:
		scale, units = uint64(binary.BigEndian.Uint32(b[20:24])), binary.BigEndian.Uint64(b[24:32])
	}
	if scale == 0 {
		return 0
	}
	return time.Duration(float64(units) / float64(scale) * float64(time.Second))
}
