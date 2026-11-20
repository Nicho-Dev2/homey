package jellyfin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClient(t *testing.T) {
	refreshed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != `MediaBrowser Token="key"` {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/System/Info":
			w.Write([]byte(`{"ServerName":"Mac","Version":"10.11.2"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/Library/Refresh":
			refreshed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/", "key")
	if err != nil {
		t.Fatal(err)
	}
	if name, err := c.Check(context.Background()); err != nil || name != "Mac (Jellyfin 10.11.2)" {
		t.Errorf("check: %q %v", name, err)
	}
	if err := c.Refresh(context.Background()); err != nil || !refreshed {
		t.Errorf("refresh: %v %v", refreshed, err)
	}
	bad, _ := New(srv.URL, "nope")
	if _, err := bad.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("bad key: %v", err)
	}
	if _, err := New("localhost:8096", "k"); err == nil {
		t.Error("address without http:// accepted")
	}
}
