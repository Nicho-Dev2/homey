package downloads

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
)

// fakeQBit acts like qBittorrent's sign-in and version endpoints.
func fakeQBit(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_ = r.ParseForm()
			if r.Form.Get("username") == "admin" && r.Form.Get("password") == "secret" {
				http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s1", Path: "/"})
				_, _ = w.Write([]byte("Ok."))
				return
			}
			_, _ = w.Write([]byte("Fails."))
		case "/api/v2/torrents/createCategory":
			w.WriteHeader(http.StatusConflict) // already there
		case "/api/v2/torrents/add":
			_ = r.ParseForm()
			if r.Form.Get("category") != "oneforarr" || !strings.HasPrefix(r.Form.Get("urls"), "magnet:") {
				_, _ = w.Write([]byte("Fails."))
				return
			}
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			if r.URL.Query().Get("category") != "oneforarr" {
				_, _ = w.Write([]byte("[]"))
				return
			}
			_, _ = w.Write([]byte(`[{"hash":"abc","name":"A","progress":1,"state":"stalledUP","content_path":"/data/downloads/A","tags":"ofa-movie-1, other"},
				{"hash":"def","name":"B","progress":1,"state":"checkingUP","tags":""}]`))
		case "/api/v2/app/version":
			if c, err := r.Cookie("SID"); err != nil || c.Value != "s1" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_, _ = w.Write([]byte("v5.1.2"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestVersion(t *testing.T) {
	srv := fakeQBit(t)
	defer srv.Close()
	q, err := NewQBittorrent(srv.URL+"/", "admin", "secret")
	if err != nil {
		t.Fatal(err)
	}
	v, err := q.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != "v5.1.2" {
		t.Errorf("version = %q", v)
	}
}

func TestAdd(t *testing.T) {
	srv := fakeQBit(t)
	defer srv.Close()
	q, _ := NewQBittorrent(srv.URL, "admin", "secret")
	ctx := context.Background()
	if err := q.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if err := q.EnsureCategory(ctx, "oneforarr", "/data/downloads"); err != nil {
		t.Fatalf("existing category: %v", err)
	}
	if err := q.Add(ctx, "magnet:?xt=urn:btih:abc", "oneforarr", "ofa-movie-1"); err != nil {
		t.Fatal(err)
	}
	if err := q.Add(ctx, "https://x.invalid/a.torrent", "oneforarr", ""); err == nil {
		t.Fatal("refused add reported as success")
	}
}

// qBittorrent 5.2+ answers a good sign-in with 204 and no text.
func TestTorrents(t *testing.T) {
	srv := fakeQBit(t)
	defer srv.Close()
	q, _ := NewQBittorrent(srv.URL, "admin", "secret")
	list, err := q.Torrents(context.Background(), "oneforarr")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || !list[0].Done() || list[1].Done() || !list[0].HasTag("ofa-movie-1") || list[1].HasTag("ofa-movie-1") {
		t.Errorf("list: %+v", list)
	}
}

func TestLogin204(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("password") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s1", Path: "/"})
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	q, _ := NewQBittorrent(srv.URL, "admin", "secret")
	if err := q.Login(context.Background()); err != nil {
		t.Fatalf("204 sign-in: %v", err)
	}
	bad, _ := NewQBittorrent(srv.URL, "admin", "nope")
	if err := bad.Login(context.Background()); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("401 sign-in: %v", err)
	}
}

func TestWrongPassword(t *testing.T) {
	srv := fakeQBit(t)
	defer srv.Close()
	q, _ := NewQBittorrent(srv.URL, "admin", "nope")
	_, err := q.Version(context.Background())
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("got %v", err)
	}
}

func TestBadAddress(t *testing.T) {
	for _, a := range []string{"", "gluetun:8080", "ftp://x"} {
		if _, err := NewQBittorrent(a, "", ""); err == nil {
			t.Errorf("%q accepted", a)
		}
	}
}

// Connect signs in once and reuses the session; an expired session (403)
// signs in again by itself.
func TestConnectReusesSession(t *testing.T) {
	logins, valid := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			logins++
			valid = fmt.Sprintf("s%d", logins)
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: valid, Path: "/"})
			w.WriteHeader(http.StatusNoContent)
		default:
			if c, err := r.Cookie("SID"); err != nil || c.Value != valid {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Write([]byte(`[]`))
		}
	}))
	defer srv.Close()
	var set store.Settings
	set.QBittorrent.URL, set.QBittorrent.Category = srv.URL, "c"
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		qb, err := Connect(ctx, set)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := qb.Torrents(ctx, "c"); err != nil {
			t.Fatal(err)
		}
	}
	if logins != 1 {
		t.Errorf("signed in %d times, want 1", logins)
	}
	valid = "expired" // qBittorrent restarted
	qb, _ := Connect(ctx, set)
	if _, err := qb.Torrents(ctx, "c"); err != nil || logins != 2 {
		t.Errorf("after expiry: %v, %d sign-ins", err, logins)
	}
}
