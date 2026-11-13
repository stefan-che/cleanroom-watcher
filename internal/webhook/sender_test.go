package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestComputeHMACKnownVector(t *testing.T) {
	// Well-known HMAC-SHA256 test vector.
	got := computeHMAC("key", []byte("The quick brown fox jumps over the lazy dog"))
	want := "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"
	if got != want {
		t.Fatalf("computeHMAC = %s, want %s", got, want)
	}
}

type captured struct {
	signature   string
	contentType string
	body        []byte
}

func newCaptureServer(t *testing.T, status int) (*httptest.Server, *captured) {
	t.Helper()
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		c.signature = r.Header.Get("X-Signature")
		c.contentType = r.Header.Get("Content-Type")
		c.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

func TestSendSignsBodyWithSecret(t *testing.T) {
	srv, got := newCaptureServer(t, http.StatusOK)
	payload := EventPayload{
		Network: "tron", Currency: "USDT", TxID: "abc", Address: "TXYZ",
		From: "TFROM", To: "TXYZ", Amount: "1.5", Direction: "IN", Ts: "2026-01-01T00:00:00Z",
	}

	code, err := NewSender().Send(context.Background(), srv.URL, "s3cret", payload, 5)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if got.contentType != "application/json" {
		t.Errorf("Content-Type = %q", got.contentType)
	}

	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(got.body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got.signature != want {
		t.Errorf("X-Signature = %q, want %q", got.signature, want)
	}

	var decoded EventPayload
	if err := json.Unmarshal(got.body, &decoded); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if decoded != payload {
		t.Errorf("payload round-trip mismatch: %+v", decoded)
	}
}

func TestSendWithoutSecretOmitsSignature(t *testing.T) {
	srv, got := newCaptureServer(t, http.StatusAccepted)

	code, err := NewSender().Send(context.Background(), srv.URL, "", EventPayload{TxID: "x"}, 0)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", code)
	}
	if got.signature != "" {
		t.Errorf("X-Signature = %q, want empty", got.signature)
	}
}

func TestSendReturnsNon2xxStatus(t *testing.T) {
	srv, _ := newCaptureServer(t, http.StatusInternalServerError)

	code, err := NewSender().Send(context.Background(), srv.URL, "k", EventPayload{}, 5)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", code)
	}
}

func TestSendUnreachableURLReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	if _, err := NewSender().Send(context.Background(), url, "", EventPayload{}, 1); err == nil {
		t.Fatal("expected error for closed server")
	}
}
