package blockchair

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const baseURL = "https://api.blockchair.com"

// ── address dashboard (native ETH calls + UTXO) ───────────────────────────────

type addressData struct {
	Address struct {
		Balance          interface{} `json:"balance"`
		TransactionCount int         `json:"transaction_count"`
	} `json:"address"`
	// UTXO chains: array of tx hash strings.
	// Ethereum: null (native transfers come in Calls).
	RawTransactions json.RawMessage `json:"transactions"`
	// Calls is the Ethereum internal ETH transfer list.
	// Value is in wei — divide by bc.Decimals (1e18) to get ETH.
	// Does NOT contain ERC-20 token transfers.
	Calls []Call `json:"calls"`
}

// Call is an internal ETH value transfer from Blockchair's address dashboard.
// Value is in wei. Fields TokenSymbol / TokenAddress do NOT exist in the API —
// all calls are native-ETH transfers, not token transfers.
type Call struct {
	TransactionHash string  `json:"transaction_hash"`
	Index           string  `json:"index"`
	Time            string  `json:"time"`
	Sender          string  `json:"sender"`
	Recipient       string  `json:"recipient"`
	Value           float64 `json:"value"` // wei as float64
	ValueUsd        float64 `json:"value_usd"`
	Transferred     bool    `json:"transferred"` // false = failed call, skip
}

// UTXOInput is a Bitcoin-style transaction input.
// Recipient here is the address whose coins are being spent (the FROM address).
type UTXOInput struct {
	Recipient string      `json:"recipient"`
	Value     interface{} `json:"value"`
}

// UTXOOutput is a Bitcoin-style transaction output.
type UTXOOutput struct {
	Recipient string      `json:"recipient"`
	Value     interface{} `json:"value"`
}

// UTXOTransactionDetail holds full UTXO transaction data.
// Fetched via /{network}/dashboards/transactions/{hash,...}.
type UTXOTransactionDetail struct {
	Transaction struct {
		Hash string      `json:"hash"`
		Time string      `json:"time"`
		Fee  interface{} `json:"fee"`
	} `json:"transaction"`
	Inputs  []UTXOInput  `json:"inputs"`
	Outputs []UTXOOutput `json:"outputs"`
}

// AddressDashboard holds parsed data for a single address.
type AddressDashboard struct {
	RawBalance       float64
	TransactionCount int
	// TxHashes contains tx hashes for UTXO chains (Bitcoin, etc.).
	// Use GetTransactionDetails to fetch full input/output data.
	TxHashes []string
	// Calls contains internal ETH transfers (Ethereum only).
	// Value is in wei; divide by bc.Decimals (1e18) to get ETH.
	Calls []Call
}

type apiResponse struct {
	Data    map[string]json.RawMessage `json:"data"`
	Context struct {
		Code int `json:"code"`
	} `json:"context"`
}

// ── ERC-20 ────────────────────────────────────────────────────────────────────

// ERC20Transaction is one token transfer from the
// /ethereum/erc-20/{token}/dashboards/address/{address} endpoint.
type ERC20Transaction struct {
	BlockID         int     `json:"block_id"`
	ID              int64   `json:"id"` // unique Blockchair event ID — use as log_index
	TransactionHash string  `json:"transaction_hash"`
	Time            string  `json:"time"`
	TokenAddress    string  `json:"token_address"`
	TokenName       string  `json:"token_name"`
	TokenSymbol     string  `json:"token_symbol"`
	TokenDecimals   int     `json:"token_decimals"`
	Sender          string  `json:"sender"`
	Recipient       string  `json:"recipient"`
	Value           string  `json:"value"`             // raw amount as string
	ValueApprox     float64 `json:"value_approximate"` // human-readable (already divided)
}

// ── Client ────────────────────────────────────────────────────────────────────

// Client is a minimal Blockchair API client.
type Client struct {
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new Blockchair client.
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

// GetAddressDashboard fetches native-currency data for an address.
//   - Ethereum: returns Calls (internal ETH transfers, value in wei).
//   - UTXO chains (Bitcoin etc.): returns TxHashes; use GetTransactionDetails for details.
//
// ERC-20 token transfers are NOT included — use GetERC20Transactions per token.
func (c *Client) GetAddressDashboard(ctx context.Context, network, address string) (*AddressDashboard, error) {
	reqURL := fmt.Sprintf("%s/%s/dashboards/address/%s", baseURL, network, url.PathEscape(address))
	reqURL = c.withKey(reqURL)

	body, err := c.doGet(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var apiResp apiResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}
	if apiResp.Context.Code == 402 {
		return nil, fmt.Errorf("blockchair API limit reached")
	}

	raw, ok := findKey(apiResp.Data, address)
	if !ok {
		return nil, nil
	}

	var data addressData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("unmarshal address data: %w", err)
	}

	dashboard := &AddressDashboard{
		RawBalance:       ParseValue(data.Address.Balance),
		TransactionCount: data.Address.TransactionCount,
		Calls:            data.Calls,
	}

	// UTXO chains return an array of hash strings in "transactions".
	if len(data.RawTransactions) > 0 && string(data.RawTransactions) != "null" {
		var hashes []string
		if err := json.Unmarshal(data.RawTransactions, &hashes); err == nil {
			dashboard.TxHashes = hashes
		}
	}

	return dashboard, nil
}

// GetERC20Transactions fetches ERC-20 token transfer history for an address.
// Endpoint: /ethereum/erc-20/{tokenContract}/dashboards/address/{address}
// Blockchair returns each event twice — this method deduplicates by ID.
// Events with Value == "0" (approve calls) are included; callers should filter them.
func (c *Client) GetERC20Transactions(ctx context.Context, tokenContract, address string) ([]ERC20Transaction, error) {
	reqURL := fmt.Sprintf("%s/ethereum/erc-20/%s/dashboards/address/%s",
		baseURL,
		url.PathEscape(tokenContract),
		url.PathEscape(address),
	)
	reqURL = c.withKey(reqURL)

	body, err := c.doGet(ctx, reqURL)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Data    map[string]json.RawMessage `json:"data"`
		Context struct {
			Code int `json:"code"`
		} `json:"context"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal erc20 response: %w", err)
	}
	if resp.Context.Code == 402 {
		return nil, fmt.Errorf("blockchair API limit reached")
	}

	raw, ok := findKey(resp.Data, address)
	if !ok {
		return nil, nil
	}

	var data struct {
		Transactions []ERC20Transaction `json:"transactions"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("unmarshal erc20 address data: %w", err)
	}

	// Deduplicate: Blockchair returns each event twice with identical ID.
	seen := make(map[int64]struct{}, len(data.Transactions))
	out := data.Transactions[:0]
	for _, tx := range data.Transactions {
		if _, dup := seen[tx.ID]; dup {
			continue
		}
		seen[tx.ID] = struct{}{}
		out = append(out, tx)
	}

	return out, nil
}

// GetTransactionDetails fetches full UTXO transaction details for the given hashes.
// Blockchair allows up to 10 hashes per request; this method batches automatically.
// Returns a map keyed by lowercase transaction hash.
func (c *Client) GetTransactionDetails(ctx context.Context, network string, hashes []string) (map[string]*UTXOTransactionDetail, error) {
	if len(hashes) == 0 {
		return nil, nil
	}

	result := make(map[string]*UTXOTransactionDetail, len(hashes))

	for i := 0; i < len(hashes); i += 10 {
		end := i + 10
		if end > len(hashes) {
			end = len(hashes)
		}
		batch := hashes[i:end]

		reqURL := fmt.Sprintf("%s/%s/dashboards/transactions/%s",
			baseURL, network, strings.Join(batch, ","))
		reqURL = c.withKey(reqURL)

		body, err := c.doGet(ctx, reqURL)
		if err != nil {
			return result, err
		}

		var apiResp struct {
			Data map[string]*UTXOTransactionDetail `json:"data"`
		}
		if err := json.Unmarshal(body, &apiResp); err != nil {
			return result, fmt.Errorf("unmarshal transaction details: %w", err)
		}

		for k, v := range apiResp.Data {
			result[strings.ToLower(k)] = v
		}
	}

	return result, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

func (c *Client) withKey(reqURL string) string {
	if c.apiKey == "" {
		return reqURL
	}
	if strings.Contains(reqURL, "?") {
		return reqURL + "&key=" + c.apiKey
	}
	return reqURL + "?key=" + c.apiKey
}

func (c *Client) doGet(ctx context.Context, reqURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	if resp.StatusCode == http.StatusPaymentRequired {
		return nil, fmt.Errorf("blockchair API limit reached (402)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("blockchair returned status %d: %s", resp.StatusCode, firstN(string(body), 200))
	}

	return body, nil
}

// findKey looks up address in the data map, case-insensitively.
func findKey(m map[string]json.RawMessage, address string) (json.RawMessage, bool) {
	if v, ok := m[address]; ok {
		return v, true
	}
	lc := strings.ToLower(address)
	for k, v := range m {
		if strings.ToLower(k) == lc {
			return v, true
		}
	}
	return nil, false
}

func equalFold(a, b string) bool {
	return strings.EqualFold(a, b)
}

// ParseValue converts a Blockchair value (string or float64) to float64.
func ParseValue(v interface{}) float64 {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return val
	case string:
		var f float64
		fmt.Sscanf(val, "%f", &f)
		return f
	case json.Number:
		f, _ := val.Float64()
		return f
	}
	return 0
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
