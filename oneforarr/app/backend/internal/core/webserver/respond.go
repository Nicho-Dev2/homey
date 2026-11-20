// Package webserver holds the small helpers every API route uses, and serves
// the built web interface.
package webserver

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
)

// JSON writes v as a JSON response with the given status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write response: %v", err)
	}
}

// Error writes {"error": message}. The front end shows message as is, so
// write it for a person.
func Error(w http.ResponseWriter, status int, message string) {
	if n, ok := w.(errorNoter); ok {
		n.noteError(message)
	}
	JSON(w, status, map[string]string{"error": message})
}

// errorNoter is the response writer from Logged, which puts the message of
// a failed request in the log line.
type errorNoter interface{ noteError(string) }

// maxBody is the largest request body any route accepts.
const maxBody = 1 << 20 // 1 MiB

// Decode reads a JSON request body into v. It refuses unknown fields so a
// typo in the front end shows up at once instead of being ignored.
func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("the request was empty")
		}
		return errors.New("the request could not be read")
	}
	return nil
}

// JSONOnly refuses changes (POST, PUT, PATCH, DELETE) unless they are sent as
// JSON. A web page on another site can't send a JSON request to us without
// the browser asking first, so this, together with the SameSite session
// cookie, stops other sites from acting as a signed-in user.
func JSONOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				Error(w, http.StatusUnsupportedMediaType, "Requests must be sent as JSON.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Logged logs each API request that fails, with its status.
func Logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		// Failed requests, with the reason the page was shown. Signed-out
		// checks (401) are normal and left out.
		if sw.status >= 400 && sw.status != http.StatusUnauthorized {
			log.Printf("%s %s -> %d: %s", r.Method, r.URL.Path, sw.status, sw.message)
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status  int
	message string
}

func (s *statusWriter) noteError(m string) { s.message = m }

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
