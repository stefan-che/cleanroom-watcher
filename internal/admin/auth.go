package admin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const cookieName = "admin_session"
const sessionDuration = 8 * time.Hour

type sessionPayload struct {
	Username string `json:"u"`
	Expires  int64  `json:"exp"`
}

func (h *Handler) signSession(p sessionPayload) string {
	data, _ := json.Marshal(p)
	b64 := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, []byte(h.cfg.Admin.SessionSecret))
	mac.Write([]byte(b64))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return b64 + "." + sig
}

func (h *Handler) verifySession(val string) bool {
	parts := strings.SplitN(val, ".", 2)
	if len(parts) != 2 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(h.cfg.Admin.SessionSecret))
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return false
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return false
	}
	var p sessionPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return false
	}
	return time.Now().Unix() < p.Expires
}

func (h *Handler) setSession(w http.ResponseWriter, username string) {
	val := h.signSession(sessionPayload{
		Username: username,
		Expires:  time.Now().Add(sessionDuration).Unix(),
	})
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    val,
		Path:     h.basePath,
		HttpOnly: true,
		MaxAge:   int(sessionDuration.Seconds()),
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handler) clearSession(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:   cookieName,
		Value:  "",
		Path:   h.basePath,
		MaxAge: -1,
	})
}

func (h *Handler) isAuthenticated(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	return h.verifySession(c.Value)
}

func (h *Handler) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.isAuthenticated(r) {
			http.Redirect(w, r, h.basePath+"/login", http.StatusFound)
			return
		}
		next(w, r)
	}
}

func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if h.isAuthenticated(r) {
		http.Redirect(w, r, h.basePath+"/addresses", http.StatusFound)
		return
	}
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		user := r.FormValue("username")
		pass := r.FormValue("password")
		if user == h.cfg.Admin.Username && pass == h.cfg.Admin.Password {
			h.setSession(w, user)
			http.Redirect(w, r, h.basePath+"/addresses", http.StatusFound)
			return
		}
		h.render(w, "login.html", map[string]interface{}{
			"Error": "Invalid username or password",
		})
		return
	}
	h.render(w, "login.html", nil)
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	h.clearSession(w)
	http.Redirect(w, r, h.basePath+"/login", http.StatusFound)
}
