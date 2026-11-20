package downloads

import (
	"fmt"
	"strings"
	"syscall"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
)

// FreeBytes is the free space on the drive that holds path.
func FreeBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}

const gb = 1 << 30

// CheckSpace refuses a download of size bytes (0 = unknown) when it would
// leave less than the "keep free" amount on the downloads drive. When the
// free space can't be read, it doesn't stand in the way.
func CheckSpace(set store.Settings, size int64) error {
	keep := int64(set.Folders.MinFreeGB) * gb
	if keep <= 0 || set.Folders.Downloads == "" {
		return nil
	}
	free, err := FreeBytes(set.Folders.Downloads)
	if err != nil {
		return nil
	}
	if free-size < keep {
		need := ""
		if size > 0 {
			need = fmt.Sprintf(" This release needs %.1f GB.", float64(size)/gb)
		}
		return fmt.Errorf("Not enough space: %.0f GB free on the media drive and Settings keeps %d GB free.%s", float64(free)/gb, set.Folders.MinFreeGB, need)
	}
	return nil
}

// LinkKind names the kind of link sent to qBittorrent, for logs and the
// Activity page. A magnet without trackers can only find peers through DHT,
// so it can sit at "Downloading metadata" for a long time.
func LinkKind(link string) string {
	switch {
	case strings.HasPrefix(link, "magnet:") && strings.Contains(link, "tr="):
		return "magnet"
	case strings.HasPrefix(link, "magnet:"):
		return "magnet without trackers"
	default:
		return "torrent file"
	}
}
