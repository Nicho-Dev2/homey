package store

import (
	"path"
	"strings"
)

// ToApp turns a path as qBittorrent reports it into the same path as this
// app sees it, using the path mapping in Folders.
//
//	QBitRoot /data, AppRoot /Volumes/MyDrive/Media:
//	/data/downloads/x.mkv → /Volumes/MyDrive/Media/downloads/x.mkv
func (f Folders) ToApp(p string) string { return swapRoot(p, f.QBitRoot, f.AppRoot) }

// ToQBit is the reverse: a path as this app sees it, as qBittorrent sees it.
func (f Folders) ToQBit(p string) string { return swapRoot(p, f.AppRoot, f.QBitRoot) }

func swapRoot(p, from, to string) string {
	p = path.Clean(p)
	from, to = path.Clean(from), path.Clean(to)
	if from == "." || to == "." || from == to {
		return p
	}
	if p == from {
		return to
	}
	if strings.HasPrefix(p, strings.TrimSuffix(from, "/")+"/") {
		return path.Join(to, strings.TrimPrefix(p, from))
	}
	return p
}

// Repath changes every saved path under from so it starts with to instead:
// the Movies/Shows/Downloads folders, the path mapping and the file of each
// movie and episode. Use it when the media drive is seen under another path,
// e.g. moving from `make run` on the Mac (/Volumes/…/Media) into Docker
// (/data). It returns how many paths changed. No files are moved.
func (d *Data) Repath(from, to string) int {
	changed := 0
	swap := func(p *string) {
		if *p == "" {
			return
		}
		if n := swapRoot(*p, from, to); n != *p {
			changed++
			*p = n
		}
	}
	f := &d.Settings.Folders
	for _, p := range []*string{&f.Movies, &f.Shows, &f.Downloads, &f.AppRoot} {
		swap(p)
	}
	for i := range d.Movies {
		if d.Movies[i].File != nil {
			swap(&d.Movies[i].File.Path)
		}
	}
	for i := range d.Shows {
		for j := range d.Shows[i].Episodes {
			if e := &d.Shows[i].Episodes[j]; e.File != nil {
				swap(&e.File.Path)
			}
		}
	}
	return changed
}
