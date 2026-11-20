package downloads

import (
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
)

// A dead download is one nobody is sharing: no seeders connected, nothing
// downloaded, hour after hour. Indexers sometimes list old releases with
// seeders that aren't there any more. The movies and shows services give
// up on such a download after the "give up" hours from Settings: they
// remove it from qBittorrent, block that release, and search again.

// waiting are the states in which a torrent is trying to download. Queued
// and paused torrents aren't trying, so they never count as dead.
var waiting = map[string]bool{
	"downloading": true, "stalledDL": true, "metaDL": true,
	"forcedDL": true, "forcedMetaDL": true,
}

// NoteStuck updates d.StuckSince and d.Downloaded from the torrent's state:
// it starts the clock when the torrent is trying but has no seeders and no
// new bytes, and resets it whenever there are seeders or progress.
func NoteStuck(d *store.Download, t Torrent, now time.Time) {
	moving := t.NumSeeds > 0 || t.DLSpeed > 0 || t.Downloaded > d.Downloaded
	d.Downloaded = t.Downloaded
	switch {
	case !waiting[t.State] || moving:
		d.StuckSince = time.Time{}
	case d.StuckSince.IsZero():
		d.StuckSince = now
	}
}

// Dead says whether it's time to give up on d.
func Dead(d store.Download, giveUpHours int, now time.Time) bool {
	return giveUpHours > 0 && !d.StuckSince.IsZero() &&
		now.Sub(d.StuckSince) >= time.Duration(giveUpHours)*time.Hour
}
