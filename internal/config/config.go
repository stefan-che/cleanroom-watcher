package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

type TronNodeConfig struct {
	URL    string `json:"url"`
	APIKey string `json:"api_key"`
}

// ERC20Token describes an ERC-20 token to monitor on an EVM chain.
type ERC20Token struct {
	Contract string `json:"contract"` // token contract address, e.g. "0xdAC17F..."
	Symbol   string `json:"symbol"`   // e.g. "USDT"
	Decimals int    `json:"decimals"` // e.g. 6
}

// BlockchainConfig defines how a single blockchain is monitored.
type BlockchainConfig struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	Source         string  `json:"source"` // "tron_node" | "blockchair"
	Enabled        bool    `json:"enabled"`
	NativeCurrency string  `json:"native_currency"`
	Decimals       float64 `json:"decimals"`
	ExplorerTxURL  string  `json:"explorer_tx_url"`
	// PollIntervalSec overrides blockchair.poll_interval_sec for this chain only.
	// 0 means use the global default.
	PollIntervalSec int `json:"poll_interval_sec"`
	// ERC20Tokens lists ERC-20 tokens to monitor on this chain (EVM only).
	ERC20Tokens []ERC20Token `json:"erc20_tokens"`
}

// WorkHoursConfig defines the time window within which Blockchair polling is active.
// All times are interpreted as UTC. When Enabled is false, polling runs 24/7.
type WorkHoursConfig struct {
	Enabled  bool   `json:"enabled"`
	StartUTC string `json:"start_utc"` // "HH:MM"
	EndUTC   string `json:"end_utc"`   // "HH:MM"
	// WorkDays lists allowed weekday numbers: 0=Sunday, 1=Monday, …, 6=Saturday.
	WorkDays []int `json:"work_days"`
}

// BlockchairConfig holds Blockchair API settings.
type BlockchairConfig struct {
	APIKey          string          `json:"api_key"`
	PollIntervalSec int             `json:"poll_interval_sec"`
	RequestDelayMs  int             `json:"request_delay_ms"`
	WorkHours       WorkHoursConfig `json:"work_hours"`
}

type AdminConfig struct {
	Path          string `json:"path"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	SessionSecret string `json:"session_secret"`
}

// Enabled returns true when all required admin fields are set.
func (a AdminConfig) Enabled() bool {
	return a.Path != "" && a.Username != "" && a.Password != ""
}

type Config struct {
	TronNodes []TronNodeConfig `json:"tron_nodes"`

	USDTContract  string `json:"usdt_contract"`
	DBDSN         string `json:"db_dsn"`
	RedisAddr     string `json:"redis_addr"`
	RedisPassword string `json:"redis_password"`
	RedisDB       int    `json:"redis_db"`

	Scanner     ScannerConfig      `json:"scanner"`
	API         APIConfig          `json:"api"`
	Admin       AdminConfig        `json:"admin"`
	Webhooks    []WebhookConfig    `json:"webhooks"`
	Blockchains []BlockchainConfig `json:"blockchains"`
	Blockchair  BlockchairConfig   `json:"blockchair"`

	// WebhookConcurrency is the number of concurrent webhook delivery workers.
	WebhookConcurrency int `json:"webhook_concurrency"`
}

type ScannerConfig struct {
	StartBlock           int64   `json:"start_block"`
	Confirmations        int64   `json:"confirmations"`
	ScanStep             int64   `json:"scan_step"`
	ScanWorkers          int     `json:"scan_workers"`
	ScanIntervalMS       int     `json:"scan_interval_ms"`
	WatchDefaultTTLHours int     `json:"watch_default_ttl_hours"`
	MinTRXAmount         float64 `json:"min_trx_amount"`
	MinUSDTAmount        float64 `json:"min_usdt_amount"`
}

type APIConfig struct {
	Listen  string   `json:"listen"`
	APIKeys []string `json:"api_keys"`
}

type WebhookConfig struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Secret     string `json:"secret"`
	TimeoutSec int    `json:"timeout_sec"`
	MaxRetries int    `json:"max_retries"`
}

func Load(configFile string) (*Config, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", configFile, err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config file %s: %w", configFile, err)
	}

	if len(cfg.TronNodes) == 0 {
		return nil, fmt.Errorf("tron_nodes is required and must contain at least one node")
	}
	if cfg.USDTContract == "" {
		cfg.USDTContract = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	}
	if cfg.RedisAddr == "" {
		cfg.RedisAddr = "localhost:6379"
	}
	if cfg.Scanner.Confirmations == 0 {
		cfg.Scanner.Confirmations = 20
	}
	if cfg.Scanner.ScanStep == 0 {
		cfg.Scanner.ScanStep = 10
	}
	if cfg.Scanner.ScanWorkers == 0 {
		cfg.Scanner.ScanWorkers = 20
	}
	if cfg.Scanner.ScanIntervalMS == 0 {
		cfg.Scanner.ScanIntervalMS = 2000
	}
	if cfg.Scanner.WatchDefaultTTLHours == 0 {
		cfg.Scanner.WatchDefaultTTLHours = 72
	}
	if cfg.API.Listen == "" {
		cfg.API.Listen = ":8080"
	}
	if cfg.DBDSN == "" {
		return nil, fmt.Errorf("db_dsn is required in config")
	}
	if cfg.Blockchair.PollIntervalSec == 0 {
		cfg.Blockchair.PollIntervalSec = 300
	}
	if cfg.Blockchair.RequestDelayMs == 0 {
		cfg.Blockchair.RequestDelayMs = 1200
	}
	if cfg.WebhookConcurrency == 0 {
		cfg.WebhookConcurrency = 10
	}
	if cfg.Admin.Enabled() && cfg.Admin.SessionSecret == "" {
		h := sha256.Sum256([]byte("admin-session-secret:" + cfg.Admin.Password))
		cfg.Admin.SessionSecret = hex.EncodeToString(h[:])
	}

	// Apply defaults to blockchain configs
	for i := range cfg.Blockchains {
		if cfg.Blockchains[i].Decimals == 0 {
			cfg.Blockchains[i].Decimals = 1e8
		}
	}

	return cfg, nil
}

// EffectivePollInterval returns the poll interval for a blockchain,
// falling back to the global blockchair.poll_interval_sec.
func (c *Config) EffectivePollInterval(bc BlockchainConfig) int {
	if bc.PollIntervalSec > 0 {
		return bc.PollIntervalSec
	}
	return c.Blockchair.PollIntervalSec
}

// BlockchainByID returns the blockchain config for the given id, or nil if not found.
func (c *Config) BlockchainByID(id string) *BlockchainConfig {
	for i := range c.Blockchains {
		if c.Blockchains[i].ID == id {
			return &c.Blockchains[i]
		}
	}
	return nil
}
