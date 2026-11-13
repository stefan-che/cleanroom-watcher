package tron

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// TronClient is the interface for interacting with a Tron node.
type TronClient interface {
	GetLatestBlockNumber(ctx context.Context) (int64, error)
	GetBlock(ctx context.Context, num int64) (*Block, error)
	GetTransactionInfo(ctx context.Context, txID string) (*TransactionInfo, error)
	GetTransactionInfosByBlock(ctx context.Context, num int64) ([]TransactionInfo, error)
}

// Client is an HTTP client for the Tron Full Node API.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a new Tron Full Node HTTP client.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// GetLatestBlockNumber returns the current block number on the Tron network.
func (c *Client) GetLatestBlockNumber(ctx context.Context) (int64, error) {
	var resp NowBlockResponse
	if err := c.post(ctx, "/wallet/getnowblock", nil, &resp); err != nil {
		return 0, fmt.Errorf("get now block: %w", err)
	}
	return resp.BlockHeader.RawData.Number, nil
}

// GetBlock returns the block at the given block number.
func (c *Client) GetBlock(ctx context.Context, num int64) (*Block, error) {
	req := map[string]interface{}{"num": num}
	var block Block
	if err := c.post(ctx, "/wallet/getblockbynum", req, &block); err != nil {
		return nil, fmt.Errorf("get block %d: %w", num, err)
	}
	return &block, nil
}

// GetTransactionInfo returns the transaction info for the given transaction ID.
func (c *Client) GetTransactionInfo(ctx context.Context, txID string) (*TransactionInfo, error) {
	req := map[string]string{"value": txID}
	var info TransactionInfo
	if err := c.post(ctx, "/wallet/gettransactioninfobyid", req, &info); err != nil {
		return nil, fmt.Errorf("get tx info %s: %w", txID, err)
	}
	if info.ID == "" {
		slog.Debug("empty tx info", "txid", txID)
		return nil, nil
	}
	return &info, nil
}

// GetTransactionInfosByBlock returns all transaction infos for the given block number
// in a single RPC call — use this instead of calling GetTransactionInfo per-transaction.
func (c *Client) GetTransactionInfosByBlock(ctx context.Context, num int64) ([]TransactionInfo, error) {
	req := map[string]interface{}{"num": num}
	var infos []TransactionInfo
	if err := c.post(ctx, "/wallet/gettransactioninfobyblocknum", req, &infos); err != nil {
		return nil, fmt.Errorf("get tx infos block %d: %w", num, err)
	}
	return infos, nil
}

func (c *Client) post(ctx context.Context, path string, body interface{}, out interface{}) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	} else {
		reqBody = bytes.NewReader([]byte("{}"))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}
