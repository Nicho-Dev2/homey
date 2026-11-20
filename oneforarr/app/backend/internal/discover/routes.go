package discover

import (
	"net/http"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/webserver"
)

// Routes adds GET /api/discover (?refresh=1 fetches new lists from TMDB).
func (s *Service) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/discover", func(w http.ResponseWriter, r *http.Request) {
		page, err := s.Page(r.Context(), r.URL.Query().Get("refresh") == "1")
		if err != nil {
			webserver.Error(w, http.StatusBadGateway, err.Error())
			return
		}
		webserver.JSON(w, http.StatusOK, page)
	})
}
