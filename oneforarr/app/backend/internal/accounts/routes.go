package accounts

import (
	"errors"
	"net/http"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/webserver"
)

// PublicRoutes are the routes that work before signing in.
func (s *Service) PublicRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/me", s.handleMe)
	mux.HandleFunc("POST /api/setup", s.handleSetup)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
}

type loginBody struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// handleMe tells the front end which screen to show: setup, sign-in or the app.
func (s *Service) handleMe(w http.ResponseWriter, r *http.Request) {
	d := s.Store.Read()
	signedIn := false
	if c, err := r.Cookie(CookieName); err == nil {
		signedIn = s.Valid(c.Value)
	}
	username := ""
	if signedIn && d.Owner != nil {
		username = d.Owner.Username
	}
	webserver.JSON(w, http.StatusOK, map[string]any{
		"needsSetup": d.Owner == nil,
		"signedIn":   signedIn,
		"username":   username,
	})
}

func (s *Service) handleSetup(w http.ResponseWriter, r *http.Request) {
	var b loginBody
	if err := webserver.Decode(w, r, &b); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.CreateOwner(b.Username, b.Password); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrAlreadySetUp) {
			status = http.StatusConflict
		}
		webserver.Error(w, status, err.Error())
		return
	}
	s.startSession(w, r, b.Username, b.Password)
}

func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var b loginBody
	if err := webserver.Decode(w, r, &b); err != nil {
		webserver.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	s.startSession(w, r, b.Username, b.Password)
}

func (s *Service) startSession(w http.ResponseWriter, r *http.Request, username, password string) {
	token, err := s.SignIn(username, password, clientIP(r))
	switch {
	case errors.Is(err, ErrTooMany):
		webserver.Error(w, http.StatusTooManyRequests, err.Error())
		return
	case errors.Is(err, ErrBadLogin):
		webserver.Error(w, http.StatusUnauthorized, err.Error())
		return
	case err != nil:
		webserver.Error(w, http.StatusInternalServerError, "Couldn't sign in: "+err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(SessionLength.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
	webserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(CookieName); err == nil {
		if err := s.SignOut(c.Value); err != nil {
			webserver.Error(w, http.StatusInternalServerError, "Couldn't sign out: "+err.Error())
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	webserver.JSON(w, http.StatusOK, map[string]bool{"ok": true})
}
