package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTVmaze(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/lookup/shows" && r.URL.Query().Get("thetvdb") == "999":
			w.WriteHeader(http.StatusNotFound)
		case r.URL.Path == "/lookup/shows" && r.URL.Query().Get("imdb") == "tt1":
			// TVmaze answers a lookup with a redirect to the show.
			http.Redirect(w, r, "/shows/42", http.StatusMovedPermanently)
		case r.URL.Path == "/shows/42":
			w.Write([]byte(`{"id":42,"name":"Show"}`))
		case r.URL.Path == "/shows/42/episodes":
			w.Write([]byte(`[
				{"season":1,"number":1,"name":"Pilot","airdate":"2020-01-01","airstamp":"2020-01-02T02:00:00+00:00","runtime":60,"summary":"<p>It &amp; begins.</p>"},
				{"season":1,"number":null,"name":"Special","airdate":"2020-02-01"},
				{"season":1,"number":2,"name":"Two","airdate":""}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	TVmazeBase = srv.URL

	tv := NewTVmaze()
	id, err := tv.FindShow(context.Background(), 999, "tt1")
	if err != nil || id != 42 {
		t.Fatalf("FindShow = %d, %v", id, err)
	}
	eps, err := tv.Episodes(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 2 || eps[0].Title != "Pilot" || eps[0].Overview != "It & begins." || eps[0].AirStamp.IsZero() || eps[1].AirDate != "" {
		t.Errorf("episodes: %+v", eps)
	}
	if _, err := tv.FindShow(context.Background(), 999, ""); err == nil {
		t.Error("unknown show found")
	}
}
