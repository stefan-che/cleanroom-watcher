package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadAppliesDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{
		"tron_nodes": [{"url": "http://node.invalid"}],
		"db_dsn": "user:pass@tcp(localhost:3306)/test",
		"blockchains": [{"id": "btc"}]
	}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.USDTContract != "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t" {
		t.Errorf("USDTContract = %q", cfg.USDTContract)
	}
	if cfg.RedisAddr != "localhost:6379" || cfg.API.Listen != ":8080" {
		t.Errorf("RedisAddr=%q Listen=%q", cfg.RedisAddr, cfg.API.Listen)
	}
	if cfg.Scanner.Confirmations != 20 || cfg.Scanner.WatchDefaultTTLHours != 72 {
		t.Errorf("scanner defaults not applied: %+v", cfg.Scanner)
	}
	if cfg.WebhookConcurrency != 10 || cfg.Blockchair.PollIntervalSec != 300 {
		t.Errorf("WebhookConcurrency=%d PollIntervalSec=%d", cfg.WebhookConcurrency, cfg.Blockchair.PollIntervalSec)
	}
	if cfg.Blockchains[0].Decimals != 1e8 {
		t.Errorf("blockchain decimals default = %v", cfg.Blockchains[0].Decimals)
	}
	if cfg.Admin.Enabled() || cfg.Admin.SessionSecret != "" {
		t.Errorf("admin should be disabled without credentials")
	}
}

func TestLoadDerivesAdminSessionSecret(t *testing.T) {
	cfg, err := Load(writeConfig(t, `{
		"tron_nodes": [{"url": "http://node.invalid"}],
		"db_dsn": "dsn",
		"admin": {"path": "/admin", "username": "u", "password": "p"}
	}`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Admin.Enabled() || len(cfg.Admin.SessionSecret) != 64 {
		t.Fatalf("expected derived 64-hex session secret, got %q", cfg.Admin.SessionSecret)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"no nodes": {`{"db_dsn": "dsn"}`, "tron_nodes"},
		"no dsn":   {`{"tron_nodes": [{"url": "x"}]}`, "db_dsn"},
		"bad json": {`{`, "parse config"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}

	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("expected error for missing file")
	}
}
