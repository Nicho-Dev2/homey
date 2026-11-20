// Package accounts handles the owner account: first-run setup, signing in
// and out, and checking that API requests come from a signed-in browser.
package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/webserver"
)

// CookieName is the session cookie.
const CookieName = "oneforarr_session"

// SessionLength is how long a sign-in lasts.
const SessionLength = 30 * 24 * time.Hour

// Service is the accounts part of the app.
type Service struct {
	Store *store.Store
	// Now is the clock; tests replace it.
	Now func() time.Time

	limiter failLimiter
}

// New returns the accounts service.
func New(s *store.Store) *Service {
	return &Service{Store: s, Now: time.Now}
}

// Errors shown to the person signing in.
var (
	ErrAlreadySetUp = errors.New("This app already has an owner. Sign in instead.")
	ErrBadLogin     = errors.New("That username and password don't match.")
	ErrTooMany      = errors.New("Too many tries. Wait 15 minutes and try again.")
)

// CreateOwner makes the owner account. It only works once.
func (s *Service) CreateOwner(username, password string) error {
	username = strings.TrimSpace(username)
	if err := checkNewLogin(username, password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	return s.Store.Update(func(d *store.Data) error {
		if d.Owner != nil {
			return ErrAlreadySetUp
		}
		d.Owner = &store.Owner{Username: username, PasswordHash: hash, CreatedAt: s.Now()}
		return nil
	})
}

func checkNewLogin(username, password string) error {
	if username == "" {
		return errors.New("Choose a username.")
	}
	if len(username) > 64 {
		return errors.New("Choose a username of 64 characters or fewer.")
	}
	if len(password) < 10 {
		return errors.New("Choose a password of at least 10 characters.")
	}
	if len(password) > 256 {
		return errors.New("Choose a password of 256 characters or fewer.")
	}
	return nil
}

// SignIn checks the login and returns a new session token for the cookie.
func (s *Service) SignIn(username, password, clientIP string) (string, error) {
	if !s.limiter.allow(clientIP, s.Now()) {
		return "", ErrTooMany
	}
	owner := s.Store.Read().Owner
	ok := owner != nil &&
		strings.EqualFold(owner.Username, strings.TrimSpace(username)) &&
		CheckPassword(owner.PasswordHash, password)
	if !ok {
		s.limiter.fail(clientIP, s.Now())
		return "", ErrBadLogin
	}
	s.limiter.clear(clientIP)
	return s.newSession()
}

func (s *Service) newSession() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := s.Now()
	err := s.Store.Update(func(d *store.Data) error {
		// Drop expired sessions while we're here.
		for k, sess := range d.Sessions {
			if now.After(sess.ExpiresAt) {
				delete(d.Sessions, k)
			}
		}
		d.Sessions[tokenKey(token)] = store.Session{CreatedAt: now, ExpiresAt: now.Add(SessionLength)}
		return nil
	})
	return token, err
}

// SignOut ends the session with this token.
func (s *Service) SignOut(token string) error {
	return s.Store.Update(func(d *store.Data) error {
		delete(d.Sessions, tokenKey(token))
		return nil
	})
}

// Valid reports whether token belongs to a live session.
func (s *Service) Valid(token string) bool {
	if token == "" {
		return false
	}
	sess, ok := s.Store.Read().Sessions[tokenKey(token)]
	return ok && s.Now().Before(sess.ExpiresAt)
}

// tokenKey is how a session is saved: a hash, never the token itself.
func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// RequireOwner lets a request through only with a valid session cookie.
func (s *Service) RequireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(CookieName)
		if err != nil || !s.Valid(c.Value) {
			webserver.Error(w, http.StatusUnauthorized, "Sign in first.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP is the address the request came from. We don't trust
// X-Forwarded-For: the app is meant to be reached directly on the home
// network or over Tailscale, not through a proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// failLimiter allows 10 failed sign-ins per address every 15 minutes.
type failLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

const (
	maxFails   = 10
	failWindow = 15 * time.Minute
)

func (l *failLimiter) recent(ip string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range l.fails[ip] {
		if now.Sub(t) < failWindow {
			keep = append(keep, t)
		}
	}
	return keep
}

func (l *failLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(ip, now)) < maxFails
}

func (l *failLimiter) fail(ip string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[string][]time.Time{}
	}
	l.fails[ip] = append(l.recent(ip, now), now)
}

func (l *failLimiter) clear(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, ip)
}
