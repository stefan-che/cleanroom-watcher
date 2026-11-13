package api

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stefan-che/cleanroom-watcher/internal/config"
	"github.com/stefan-che/cleanroom-watcher/internal/tronscan"
)

// emptyDriver is a minimal database/sql driver whose every query returns zero
// rows. It lets handlers run their "not found" paths without MySQL.
type emptyDriver struct{}
type emptyConn struct{}
type emptyStmt struct{}
type emptyRows struct{}

func (emptyDriver) Open(string) (driver.Conn, error)         { return emptyConn{}, nil }
func (emptyConn) Prepare(string) (driver.Stmt, error)        { return emptyStmt{}, nil }
func (emptyConn) Close() error                               { return nil }
func (emptyConn) Begin() (driver.Tx, error)                  { return nil, driver.ErrSkip }
func (emptyStmt) Close() error                               { return nil }
func (emptyStmt) NumInput() int                              { return -1 }
func (emptyStmt) Exec([]driver.Value) (driver.Result, error) { return driver.RowsAffected(0), nil }
func (emptyStmt) Query([]driver.Value) (driver.Rows, error)  { return emptyRows{}, nil }
func (emptyRows) Columns() []string                          { return []string{"id"} }
func (emptyRows) Close() error                               { return nil }
func (emptyRows) Next([]driver.Value) error                  { return io.EOF }

func init() { sql.Register("api-test-empty", emptyDriver{}) }

const testAPIKey = "test-key"

func newTestMux(t *testing.T) *http.ServeMux {
	t.Helper()
	sqlDB, err := sql.Open("api-test-empty", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })

	cfg := &config.Config{API: config.APIConfig{APIKeys: []string{testAPIKey}}}
	h := NewHandler(sqlDB, nil, nil, nil, cfg, nil, nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return mux
}

func TestGetTransactionsUnknownAddressReturns404(t *testing.T) {
	mux := newTestMux(t)
	req := httptest.NewRequest(http.MethodGet, "/api/addresses/TUnknownAddr/transactions", nil)
	req.Header.Set("X-API-Key", testAPIKey)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if body["error"] != "address not found" {
		t.Errorf("error = %q", body["error"])
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestGetTransactionsRequiresAPIKey(t *testing.T) {
	mux := newTestMux(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/addresses/TUnknownAddr/transactions", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	protected := AuthMiddleware([]string{"k1", "k2"})(ok)

	cases := []struct {
		name, path, key string
		want            int
	}{
		{"missing key", "/api/addresses", "", http.StatusUnauthorized},
		{"invalid key", "/api/addresses", "nope", http.StatusUnauthorized},
		{"valid key", "/api/addresses", "k2", http.StatusTeapot},
		{"health bypass", "/health", "", http.StatusTeapot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.key != "" {
				req.Header.Set("X-API-Key", tc.key)
			}
			rec := httptest.NewRecorder()
			protected.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}

	rec := httptest.NewRecorder()
	AuthMiddleware(nil)(ok).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/addresses", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("auth disabled: status = %d, want %d", rec.Code, http.StatusTeapot)
	}
}

func TestTrimAmount(t *testing.T) {
	cases := map[string]string{
		"1.50000000": "1.5",
		"2.00000000": "2",
		"0.00000000": "0",
		"100":        "100",
		"0.00000001": "0.00000001",
	}
	for in, want := range cases {
		if got := trimAmount(in); got != want {
			t.Errorf("trimAmount(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseIntParam(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{{"", 7}, {"25", 25}, {"0", 0}, {"-3", 7}, {"abc", 7}}
	for _, tc := range cases {
		if got := parseIntParam(tc.in, 7); got != tc.want {
			t.Errorf("parseIntParam(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestFormatTronScanTransfers(t *testing.T) {
	addr := "TMyAddress"
	got := formatTronScanTransfers(addr, []tronscan.TokenTransfer{
		{TransactionID: "in1", BlockTs: 1700000000000, FromAddress: "TOther", ToAddress: "tmyaddress", Quant: "1500000"},
		{TransactionID: "out1", FromAddress: addr, ToAddress: "TOther", Quant: "250",
			TokenInfo: tronscan.TokenInfo{Symbol: "XYZ", Decimals: 2}},
		{TransactionID: "", Quant: "1"}, // skipped
	})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}

	in := got[0]
	if in.Direction != "IN" || in.Amount != "1.5" || in.Currency != "USDT" || in.Ts != "2023-11-14T22:13:20Z" {
		t.Errorf("incoming = %+v", in)
	}
	if in.From != "tother" {
		t.Errorf("from should be lowercased, got %q", in.From)
	}

	out := got[1]
	if out.Direction != "OUT" || out.Amount != "2.5" || out.Currency != "XYZ" || out.Ts != "" {
		t.Errorf("outgoing = %+v", out)
	}
}
