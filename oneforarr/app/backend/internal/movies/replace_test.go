package movies

import (
	"os"
	"path/filepath"
	"testing"
)

// A replacement with the same quality gets the same file name: it must
// replace the old file, not fail with "already exists".
func TestPlaceOverSameName(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dl", "new.mkv")
	dst := filepath.Join(dir, "movies", "A (2000)", "A (2000) Bluray-1080p.mkv")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.MkdirAll(filepath.Dir(dst), 0o755)
	os.WriteFile(src, []byte("new copy"), 0o644)
	os.WriteFile(dst, []byte("old copy"), 0o644)
	if err := placeOver(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new copy" {
		t.Errorf("dst holds %q", b)
	}
	if _, err := os.Stat(dst + ".oneforarr-new"); err == nil {
		t.Error("temporary file left behind")
	}
}

func TestInsideFolder(t *testing.T) {
	for path, want := range map[string]bool{
		"/data/movies/A (2000)/a.mkv": true,
		"/data/movies/loose.mkv":      true,
		"/data/movies":                false,
		"/data/tv/x.mkv":              false,
		"/data/movies2/x.mkv":         false,
	} {
		if got := insideFolder(path, "/data/movies"); got != want {
			t.Errorf("%s: %v", path, got)
		}
	}
}
