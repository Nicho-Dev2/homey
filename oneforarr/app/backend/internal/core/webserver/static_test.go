package webserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// A page address answers a GET with the page, but anything else sent there
// (a POST that missed its /api/ address) must fail, not look like it worked.
func TestStaticOnlyServesPages(t *testing.T) {
	h := Static(fstest.MapFS{"index.html": {Data: []byte("<html>")}})
	for _, c := range []struct {
		method string
		want   int
	}{{http.MethodGet, http.StatusOK}, {http.MethodPost, http.StatusNotFound}, {http.MethodPut, http.StatusNotFound}} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, "/movies", nil))
		if rec.Code != c.want {
			t.Errorf("%s /movies: %d, want %d", c.method, rec.Code, c.want)
		}
	}
}
