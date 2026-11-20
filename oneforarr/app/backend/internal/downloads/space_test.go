package downloads

import (
	"strings"
	"testing"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
)

func TestCheckSpace(t *testing.T) {
	dir := t.TempDir()
	free, err := FreeBytes(dir)
	if err != nil || free <= 0 {
		t.Fatalf("free: %d %v", free, err)
	}
	var set store.Settings
	set.Folders.Downloads = dir
	if err := CheckSpace(set, 1<<40); err != nil {
		t.Errorf("limit off but refused: %v", err)
	}
	set.Folders.MinFreeGB = 1
	if err := CheckSpace(set, 0); free > 2<<30 && err != nil {
		t.Errorf("refused with %d GB free: %v", free>>30, err)
	}
	if err := CheckSpace(set, free); err == nil || !strings.Contains(err.Error(), "Not enough space") {
		t.Errorf("release as big as the free space accepted: %v", err)
	}
}

func TestLinkKind(t *testing.T) {
	for link, want := range map[string]string{
		"magnet:?xt=urn:btih:x&tr=udp://t": "magnet",
		"magnet:?xt=urn:btih:x":            "magnet without trackers",
		"https://site.example/x.torrent":   "torrent file",
	} {
		if got := LinkKind(link); got != want {
			t.Errorf("%s: %q", link, got)
		}
	}
}
