package mediacheck

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

// Matroska (.mkv) is a tree of elements, each an ID, a size and its
// contents. The parts read here:
//
//	Segment
//	├─ SeekHead: where Tracks and Tags are (Tags are often at the very end)
//	├─ Tracks → TrackEntry: number, UID, type (1 video, 2 sound), default flag
//	└─ Tags → Tag → Targets (TagTrackUID) + SimpleTag (TagName "DURATION",
//	   TagString "00:47:40.090000000")
const (
	idEBML        = 0x1A45DFA3
	idSegment     = 0x18538067
	idSeekHead    = 0x114D9B74
	idSeek        = 0x4DBB
	idSeekID      = 0x53AB
	idSeekPos     = 0x53AC
	idCluster     = 0x1F43B675
	idTracks      = 0x1654AE6B
	idTrackEntry  = 0xAE
	idTrackUID    = 0x73C5
	idTrackType   = 0x83
	idFlagDefault = 0x88
	idTags        = 0x1254C367
	idTag         = 0x7373
	idTargets     = 0x63C0
	idTagTrackUID = 0x63C5
	idSimpleTag   = 0x67C8
	idTagName     = 0x45A3
	idTagString   = 0x4487
)

// maxElement caps how much of one element is read into memory.
const maxElement = 16 << 20

type element struct {
	id        uint32
	start     int64 // where the element (its ID) starts
	data, end int64 // where its contents start and end
	sizeKnown bool
}

func readMatroska(f *os.File) (Lengths, error) {
	st, err := f.Stat()
	if err != nil {
		return Lengths{}, err
	}
	fileEnd := st.Size()

	// The EBML header, then the Segment.
	head, err := readElement(f, 0, fileEnd)
	if err != nil || head.id != idEBML {
		return Lengths{}, ErrUnknown
	}
	seg, err := readElement(f, head.end, fileEnd)
	if err != nil || seg.id != idSegment {
		return Lengths{}, ErrUnknown
	}

	// Find Tracks and Tags: directly before the first Cluster, or through
	// the SeekHead.
	var tracksAt, tagsAt []int64
	for off := seg.data; off < seg.end; {
		el, err := readElement(f, off, seg.end)
		if err != nil || el.id == idCluster || !el.sizeKnown {
			break
		}
		switch el.id {
		case idTracks:
			tracksAt = append(tracksAt, el.start)
		case idTags:
			tagsAt = append(tagsAt, el.start)
		case idSeekHead:
			body, err := contents(f, el)
			if err != nil {
				break
			}
			for _, seek := range children(body) {
				if seek.id != idSeek {
					continue
				}
				var id uint32
				pos := int64(-1)
				for _, c := range children(seek.body) {
					switch c.id {
					case idSeekID:
						id = uint32(readUint(c.body))
					case idSeekPos:
						pos = seg.data + int64(readUint(c.body))
					}
				}
				switch {
				case pos < 0:
				case id == idTracks:
					tracksAt = append(tracksAt, pos)
				case id == idTags:
					tagsAt = append(tagsAt, pos)
				}
			}
		}
		off = el.end
	}

	// The tracks: which UID is the video, which the main sound.
	var videoUID, audioUID, firstAudioUID uint64
	for _, at := range unique(tracksAt) {
		body, err := elementContents(f, at, seg.end, idTracks)
		if err != nil {
			continue
		}
		for _, te := range children(body) {
			if te.id != idTrackEntry {
				continue
			}
			var uid, typ uint64
			isDefault := true // the flag is on unless the file says otherwise
			for _, c := range children(te.body) {
				switch c.id {
				case idTrackUID:
					uid = readUint(c.body)
				case idTrackType:
					typ = readUint(c.body)
				case idFlagDefault:
					isDefault = readUint(c.body) == 1
				}
			}
			switch {
			case typ == 1 && videoUID == 0:
				videoUID = uid
			case typ == 2 && firstAudioUID == 0:
				firstAudioUID = uid
				if isDefault {
					audioUID = uid
				}
			case typ == 2 && audioUID == 0 && isDefault:
				audioUID = uid
			}
		}
		break
	}
	if audioUID == 0 {
		audioUID = firstAudioUID
	}
	if videoUID == 0 || audioUID == 0 {
		return Lengths{}, ErrUnknown
	}

	// The tags: each track's DURATION.
	lengths := map[uint64]time.Duration{}
	for _, at := range unique(tagsAt) {
		body, err := elementContents(f, at, seg.end, idTags)
		if err != nil {
			continue
		}
		for _, tag := range children(body) {
			if tag.id != idTag {
				continue
			}
			var uids []uint64
			var length time.Duration
			for _, c := range children(tag.body) {
				switch c.id {
				case idTargets:
					for _, t := range children(c.body) {
						if t.id == idTagTrackUID {
							uids = append(uids, readUint(t.body))
						}
					}
				case idSimpleTag:
					var name, value string
					for _, s := range children(c.body) {
						switch s.id {
						case idTagName:
							name = string(trimNul(s.body))
						case idTagString:
							value = string(trimNul(s.body))
						}
					}
					if strings.EqualFold(name, "DURATION") {
						length, _ = parseDuration(value)
					}
				}
			}
			if length > 0 {
				for _, uid := range uids {
					lengths[uid] = length
				}
			}
		}
	}
	video, audio := lengths[videoUID], lengths[audioUID]
	if video == 0 || audio == 0 {
		return Lengths{}, ErrUnknown
	}
	return Lengths{Video: video, Audio: audio}, nil
}

// readElement reads the ID and size of the element at off.
func readElement(f *os.File, off, limit int64) (element, error) {
	var buf [12]byte
	n, _ := f.ReadAt(buf[:], off)
	id, idLen := readVint(buf[:n], true)
	if idLen == 0 {
		return element{}, errors.New("bad element ID")
	}
	size, sizeLen := readVint(buf[idLen:n], false)
	if sizeLen == 0 {
		return element{}, errors.New("bad element size")
	}
	el := element{id: uint32(id), start: off, data: off + int64(idLen+sizeLen)}
	allOnes := uint64(1)<<(7*sizeLen) - 1
	if size == allOnes { // "unknown size": runs to the end of its parent
		el.end = limit
	} else {
		el.end, el.sizeKnown = el.data+int64(size), true
	}
	if el.end > limit {
		el.end = limit
	}
	return el, nil
}

// readVint reads an EBML variable-length number. IDs keep their length
// marker; sizes drop it. It returns the value and how many bytes it took.
func readVint(b []byte, keepMarker bool) (uint64, int) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0
	}
	length := 1
	for mask := byte(0x80); b[0]&mask == 0; mask >>= 1 {
		length++
	}
	if length > 8 || len(b) < length || (keepMarker && length > 4) {
		return 0, 0
	}
	v := uint64(b[0])
	if !keepMarker {
		v &= uint64(0xFF >> length)
	}
	for _, c := range b[1:length] {
		v = v<<8 | uint64(c)
	}
	return v, length
}

func contents(f *os.File, el element) ([]byte, error) {
	if !el.sizeKnown || el.end-el.data > maxElement {
		return nil, ErrUnknown
	}
	buf := make([]byte, el.end-el.data)
	return buf, readAt(f, el.data, buf)
}

// elementContents reads the element at off, if it is the one expected.
func elementContents(f *os.File, off, limit int64, want uint32) ([]byte, error) {
	el, err := readElement(f, off, limit)
	if err != nil || el.id != want {
		return nil, ErrUnknown
	}
	return contents(f, el)
}

type child struct {
	id   uint32
	body []byte
}

// children splits an element's contents (in memory) into its elements.
func children(b []byte) []child {
	var out []child
	for len(b) > 0 {
		id, idLen := readVint(b, true)
		if idLen == 0 {
			break
		}
		size, sizeLen := readVint(b[idLen:], false)
		if sizeLen == 0 {
			break
		}
		start := idLen + sizeLen
		if size > uint64(len(b)-start) {
			break
		}
		end := start + int(size)
		out = append(out, child{id: uint32(id), body: b[start:end]})
		b = b[end:]
	}
	return out
}

func readUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func trimNul(b []byte) []byte {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return b
}

// parseDuration reads "00:47:40.090000000".
func parseDuration(s string) (time.Duration, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return 0, ErrUnknown
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.ParseFloat(parts[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, ErrUnknown
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(sec*float64(time.Second)), nil
}

func unique(xs []int64) []int64 {
	var out []int64
	seen := map[int64]bool{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
