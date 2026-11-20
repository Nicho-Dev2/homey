package accounts

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nicho-Dev2/homey/oneforarr/app/backend/internal/core/store"
)

func newService(t *testing.T) *Service {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	return New(db)
}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !CheckPassword(h, "correct horse battery") {
		t.Error("right password refused")
	}
	if CheckPassword(h, "wrong password!") {
		t.Error("wrong password accepted")
	}
	if CheckPassword("garbage", "anything") {
		t.Error("broken hash accepted")
	}
}

func TestOwnerOnlyOnce(t *testing.T) {
	s := newService(t)
	if err := s.CreateOwner("owner", "a long password"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateOwner("someone", "another password"); err != ErrAlreadySetUp {
		t.Fatalf("second owner: got %v, want ErrAlreadySetUp", err)
	}
}

func TestShortPasswordRefused(t *testing.T) {
	s := newService(t)
	if err := s.CreateOwner("owner", "short"); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestSignInAndSessions(t *testing.T) {
	s := newService(t)
	_ = s.CreateOwner("Owner", "a long password")

	if _, err := s.SignIn("owner", "nope nope nope", "1.2.3.4"); err != ErrBadLogin {
		t.Fatalf("bad password: got %v", err)
	}
	token, err := s.SignIn("owner", "a long password", "1.2.3.4") // username isn't case-sensitive
	if err != nil {
		t.Fatal(err)
	}
	if !s.Valid(token) {
		t.Fatal("new session not valid")
	}
	// The file holds a hash, not the token.
	for key := range s.Store.Read().Sessions {
		if key == token {
			t.Fatal("session token saved as is")
		}
	}
	// Sessions expire.
	later := time.Now().Add(SessionLength + time.Hour)
	s.Now = func() time.Time { return later }
	if s.Valid(token) {
		t.Fatal("expired session still valid")
	}
	s.Now = time.Now
	_ = s.SignOut(token)
	if s.Valid(token) {
		t.Fatal("signed-out session still valid")
	}
}

func TestTooManyTries(t *testing.T) {
	s := newService(t)
	_ = s.CreateOwner("owner", "a long password")
	for range maxFails {
		_, _ = s.SignIn("owner", "wrong password", "5.6.7.8")
	}
	if _, err := s.SignIn("owner", "a long password", "5.6.7.8"); err != ErrTooMany {
		t.Fatalf("got %v, want ErrTooMany", err)
	}
	// Another address isn't affected.
	if _, err := s.SignIn("owner", "a long password", "9.9.9.9"); err != nil {
		t.Fatalf("other address: %v", err)
	}
}

func TestRequireOwner(t *testing.T) {
	s := newService(t)
	_ = s.CreateOwner("owner", "a long password")
	token, _ := s.SignIn("owner", "a long password", "1.1.1.1")
	h := s.RequireOwner(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))

	req := httptest.NewRequest("GET", "/api/settings", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: got %d", rec.Code)
	}

	req.AddCookie(&http.Cookie{Name: CookieName, Value: token})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTeapot {
		t.Fatalf("with cookie: got %d", rec.Code)
	}
}

func TestSetupRoute(t *testing.T) {
	s := newService(t)
	mux := http.NewServeMux()
	s.PublicRoutes(mux)

	body := `{"username":"owner","password":"a long password"}`
	req := httptest.NewRequest("POST", "/api/setup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	cookie := rec.Result().Cookies()
	if len(cookie) != 1 || !cookie[0].HttpOnly || cookie[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("bad cookie: %+v", cookie)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/setup", strings.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("second setup: got %d", rec.Code)
	}
}
