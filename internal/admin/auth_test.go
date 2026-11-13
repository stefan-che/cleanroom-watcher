package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/config"
)

func newTestHandler(secret string) *Handler {
	return &Handler{
		cfg:      &config.Config{Admin: config.AdminConfig{SessionSecret: secret}},
		basePath: "/admin",
	}
}

func TestSessionSignVerifyRoundTrip(t *testing.T) {
	h := newTestHandler("secret-a")
	tok := h.signSession(sessionPayload{Username: "root", Expires: time.Now().Add(time.Hour).Unix()})
	if !h.verifySession(tok) {
		t.Fatal("freshly signed session did not verify")
	}
}

func TestSessionRejectsExpired(t *testing.T) {
	h := newTestHandler("secret-a")
	tok := h.signSession(sessionPayload{Username: "root", Expires: time.Now().Add(-time.Minute).Unix()})
	if h.verifySession(tok) {
		t.Fatal("expired session verified")
	}
}

func TestSessionRejectsForeignSecretAndTampering(t *testing.T) {
	h := newTestHandler("secret-a")
	tok := h.signSession(sessionPayload{Username: "root", Expires: time.Now().Add(time.Hour).Unix()})

	if newTestHandler("secret-b").verifySession(tok) {
		t.Error("session signed with another secret verified")
	}

	parts := strings.SplitN(tok, ".", 2)
	forged := newTestHandler("secret-b").signSession(sessionPayload{Username: "evil", Expires: time.Now().Add(time.Hour).Unix()})
	forgedPayload := strings.SplitN(forged, ".", 2)[0]
	if h.verifySession(forgedPayload + "." + parts[1]) {
		t.Error("payload swap with old signature verified")
	}

	for _, bad := range []string{"", "nodot", ".", parts[0] + "."} {
		if h.verifySession(bad) {
			t.Errorf("verifySession(%q) = true", bad)
		}
	}
}

func TestRequireAuthRedirectsWithoutCookie(t *testing.T) {
	h := newTestHandler("secret-a")
	called := false
	handler := h.requireAuth(func(w http.ResponseWriter, r *http.Request) { called = true })

	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if called {
		t.Fatal("protected handler ran without session")
	}
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/admin/login" {
		t.Fatalf("got %d Location=%q, want 302 /admin/login", rec.Code, rec.Header().Get("Location"))
	}
}

func TestRequireAuthAcceptsSessionCookie(t *testing.T) {
	h := newTestHandler("secret-a")
	rec := httptest.NewRecorder()
	h.setSession(rec, "root")
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != cookieName || !cookies[0].HttpOnly {
		t.Fatalf("unexpected cookies: %+v", cookies)
	}

	called := false
	handler := h.requireAuth(func(w http.ResponseWriter, r *http.Request) { called = true })
	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	req.AddCookie(cookies[0])
	handler(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("protected handler did not run with valid session")
	}
}
