package admin

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/config"
)

//go:embed templates
var templateFS embed.FS

// Admin styles and icon. They ship inside the image instead of loading from a CDN:
// production is locked down by the front proxy's Content-Security-Policy, and the
// administrator's browser may have no external network access.
//
// assets/tailwind.css is built from the classes used in templates/. If the templates
// gain new Tailwind classes, rebuild it:
//
//	npx tailwindcss@3.4.17 -i in.css -o internal/admin/assets/tailwind.css \
//	    --minify --content 'internal/admin/templates/*.html'
//
// where in.css is three lines: @tailwind base; @tailwind components; @tailwind utilities;
//
//go:embed assets
var assetsFS embed.FS

// Handler serves the admin web UI.
type Handler struct {
	cfg      *config.Config
	db       *sql.DB
	logDir   string
	tmpl     *template.Template
	basePath string
}

func NewHandler(cfg *config.Config, sqlDB *sql.DB, logDir string) *Handler {
	funcs := template.FuncMap{
		"dict": func(values ...interface{}) (map[string]interface{}, error) {
			if len(values)%2 != 0 {
				return nil, fmt.Errorf("dict: odd number of arguments")
			}
			m := make(map[string]interface{}, len(values)/2)
			for i := 0; i < len(values); i += 2 {
				key, ok := values[i].(string)
				if !ok {
					return nil, fmt.Errorf("dict: key must be a string")
				}
				m[key] = values[i+1]
			}
			return m, nil
		},
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "…"
		},
		"formatTime": func(t time.Time) string {
			return t.Format("02.01.2006 15:04")
		},
		"formatTimePtr": func(t *time.Time) string {
			if t == nil {
				return "—"
			}
			return t.Format("02.01.2006 15:04")
		},
		"statusClass": func(active bool, expires time.Time) string {
			if !active {
				return "badge-inactive"
			}
			if expires.Before(time.Now()) {
				return "badge-expired"
			}
			return "badge-active"
		},
		"statusText": func(active bool, expires time.Time) string {
			if !active {
				return "Inactive"
			}
			if expires.Before(time.Now()) {
				return "Expired"
			}
			return "Active"
		},
		"networkClass": func(network string) string {
			switch strings.ToLower(network) {
			case "tron":
				return "badge-tron"
			case "bitcoin":
				return "badge-btc"
			case "bnb":
				return "badge-bnb"
			case "ethereum":
				return "badge-eth"
			default:
				return "badge-default"
			}
		},
		"dirClass": func(dir string) string {
			if dir == "IN" {
				return "text-green-400"
			}
			return "text-red-400"
		},
		"isRunning": func(active bool, expires time.Time) bool {
			return active && expires.After(time.Now())
		},
		"formatFloat": func(f *float64) string {
			if f == nil {
				return "—"
			}
			return fmt.Sprintf("%.6g", *f)
		},
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },
		"gt":  func(a, b int) bool { return a > b },
		"lt":  func(a, b int) bool { return a < b },
		"jsonPretty": func(v interface{}) string {
			raw, ok := v.(json.RawMessage)
			if !ok {
				b, _ := json.Marshal(v)
				raw = b
			}
			if len(raw) == 0 || string(raw) == "null" {
				return ""
			}
			var buf bytes.Buffer
			if err := json.Indent(&buf, raw, "", "  "); err != nil {
				return string(raw)
			}
			return buf.String()
		},
	}

	tmpl := template.Must(
		template.New("").Funcs(funcs).ParseFS(templateFS, "templates/*.html"),
	)

	return &Handler{
		cfg:      cfg,
		db:       sqlDB,
		logDir:   logDir,
		tmpl:     tmpl,
		basePath: "/" + cfg.Admin.Path,
	}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	if !h.cfg.Admin.Enabled() {
		return
	}

	base := h.basePath

	// Static files are served without auth: the login page needs them too.
	// They contain no secrets, and the path is hidden behind basePath anyway.
	if assets, err := fs.Sub(assetsFS, "assets"); err == nil {
		mux.Handle(base+"/assets/", http.StripPrefix(base+"/assets/", assetHandler(http.FileServer(http.FS(assets)))))
	}

	mux.HandleFunc(base+"/login", h.handleLogin)
	mux.HandleFunc(base+"/logout", h.requireAuth(h.handleLogout))
	mux.HandleFunc(base+"/logs", h.requireAuth(h.handleLogs))
	// address detail must come before the base /addresses route
	mux.HandleFunc(base+"/addresses/action", h.requireAuth(h.handleAddressAction))
	mux.HandleFunc(base+"/addresses/", h.requireAuth(h.handleAddressDetail))
	mux.HandleFunc(base+"/addresses", h.requireAuth(h.handleAddresses))
	mux.HandleFunc(base+"/", h.requireAuth(h.handleRoot))
}

// assetHandler adds caching: the files change only with an application release,
// so the browser does not need to fetch them on every page.
func assetHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) handleRoot(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, h.basePath+"/addresses", http.StatusFound)
}

func (h *Handler) render(w http.ResponseWriter, name string, data map[string]interface{}) {
	if data == nil {
		data = map[string]interface{}{}
	}
	data["BasePath"] = h.basePath
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}
