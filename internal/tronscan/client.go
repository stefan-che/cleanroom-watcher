package tronscan

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const baseURL = "https://apilist.tronscan.org"

// TokenTransfer represents one TRC20 transfer from the TronScan API.
type TokenTransfer struct {
	TransactionID   string    `json:"transaction_id"`
	BlockTs         int64     `json:"block_ts"` // milliseconds since epoch
	FromAddress     string    `json:"from_address"`
	ToAddress       string    `json:"to_address"`
	Quant           string    `json:"quant"` // raw integer amount as decimal string
	ContractAddress string    `json:"contract_address"`
	TokenInfo       TokenInfo `json:"tokenInfo"`
}

// TokenInfo holds token metadata returned by TronScan.
type TokenInfo struct {
	Name     string `json:"tokenName"`
	Symbol   string `json:"tokenAbbr"`
	Decimals int    `json:"tokenDecimal"`
}

type trc20Response struct {
	TokenTransfers []TokenTransfer `json:"token_transfers"`
	Total          int             `json:"total"`
}

// Client is a minimal TronScan API client.
type Client struct {
	httpClient *http.Client
}

// NewClient creates a new TronScan client.
func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// GetTRC20Transfers fetches TRC20 token transfers for the given TRON address.
// Results are ordered by descending timestamp; returns up to limit records.
func (c *Client) GetTRC20Transfers(ctx context.Context, address string, limit int) ([]TokenTransfer, error) {
	params := url.Values{
		"limit":            {fmt.Sprintf("%d", limit)},
		"start":            {"0"},
		"sort":             {"-timestamp"},
		"count":            {"true"},
		"filterTokenValue": {"0"},
		"relatedAddress":   {address},
	}

	reqURL := baseURL + "/api/filter/trc20/transfers?" + params.Encode()

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

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tronscan returned status %d", resp.StatusCode)
	}

	var result trc20Response
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result.TokenTransfers, nil
}
