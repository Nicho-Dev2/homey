package watchdog

import (
	"context"
	"net/http"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/webserver"
)

// Routes adds:
//
//	GET  /api/qbittorrent/health   the latest check
//	POST /api/qbittorrent/restart  restart qBittorrent now
func (w *Watchdog) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/qbittorrent/health", func(rw http.ResponseWriter, r *http.Request) {
		webserver.JSON(rw, http.StatusOK, w.Health())
	})
	mux.HandleFunc("POST /api/qbittorrent/restart", func(rw http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := w.RestartNow(ctx); err != nil {
			webserver.Error(rw, http.StatusBadGateway, err.Error())
			return
		}
		webserver.JSON(rw, http.StatusOK, map[string]string{"message": "qBittorrent is restarting. It should be back within a minute."})
	})
}
