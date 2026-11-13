package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/blockchair"
	"github.com/stefan-che/cleanroom-watcher/internal/cache"
	"github.com/stefan-che/cleanroom-watcher/internal/config"
	"github.com/stefan-che/cleanroom-watcher/internal/db"
	"github.com/stefan-che/cleanroom-watcher/internal/tron"
	"github.com/stefan-che/cleanroom-watcher/internal/tronscan"

	"github.com/redis/go-redis/v9"
)

type Handler struct {
	db               *sql.DB
	watchCache       *cache.WatchCache
	tronClient       tron.TronClient
	rdb              *redis.Client
	cfg              *config.Config
	blockchairClient *blockchair.Client
	tronscanClient   *tronscan.Client
}

func NewHandler(
	sqlDB *sql.DB,
	watchCache *cache.WatchCache,
	tronClient tron.TronClient,
	rdb *redis.Client,
	cfg *config.Config,
	bcClient *blockchair.Client,
	tsClient *tronscan.Client,
) *Handler {
	return &Handler{
		db:               sqlDB,
		watchCache:       watchCache,
		tronClient:       tronClient,
		rdb:              rdb,
		cfg:              cfg,
		blockchairClient: bcClient,
		tronscanClient:   tsClient,
	}
}

func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	auth := AuthMiddleware(h.cfg.API.APIKeys)

	mux.Handle("POST /api/watch", auth(http.HandlerFunc(h.handleWatch)))
	mux.Handle("POST /api/unwatch", auth(http.HandlerFunc(h.handleUnwatch)))
	mux.Handle("GET /api/addresses", auth(http.HandlerFunc(h.handleListAddresses)))
	mux.Handle("GET /api/addresses/{address}", auth(http.HandlerFunc(h.handleGetAddress)))
	mux.Handle("GET /api/addresses/{address}/transactions", auth(http.HandlerFunc(h.handleGetTransactions)))
	mux.Handle("GET /api/addresses/{address}/webhooks", auth(http.HandlerFunc(h.handleListAddressWebhooks)))
	mux.Handle("POST /api/addresses/{address}/webhooks", auth(http.HandlerFunc(h.handleAddAddressWebhook)))
	mux.Handle("DELETE /api/addresses/{address}/webhooks/{id}", auth(http.HandlerFunc(h.handleDeleteAddressWebhook)))
	mux.Handle("GET /api/webhooks/deliveries", auth(http.HandlerFunc(h.handleListDeliveries)))
	mux.Handle("POST /api/webhooks/deliveries/{id}/retry", auth(http.HandlerFunc(h.handleRetryDelivery)))
	mux.Handle("GET /api/transactions", auth(http.HandlerFunc(h.handleFetchTransactions)))
	mux.Handle("GET /health", http.HandlerFunc(h.handleHealth))
}

// POST /api/watch
func (h *Handler) handleWatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Network  string `json:"network"`
		Address  string `json:"address"`
		TTLHours int    `json:"ttl_hours"`
		// Webhooks: omit or null = use global webhooks
		//           false        = disable all webhooks (polling mode)
		//           array        = per-address webhooks
		Webhooks json.RawMessage `json:"webhooks"`
		Meta     json.RawMessage `json:"meta"`
		Sync     bool            `json:"sync"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if req.Address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address is required"})
		return
	}

	if req.Network == "" {
		req.Network = "tron"
	}

	// Validate and normalize address per network
	normalizedAddress, hex40, err := h.normalizeAddress(req.Network, req.Address)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Parse webhooks field
	disableWebhooks := false
	var webhooks []db.WebhookRequest

	if req.Webhooks != nil {
		raw := strings.TrimSpace(string(req.Webhooks))
		if raw == "false" {
			disableWebhooks = true
		} else if raw != "null" && raw != "" {
			var whList []struct {
				URL        string `json:"url"`
				Secret     string `json:"secret"`
				TimeoutSec int    `json:"timeout_sec"`
				MaxRetries int    `json:"max_retries"`
			}
			if err := json.Unmarshal(req.Webhooks, &whList); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid webhooks field"})
				return
			}
			for _, wh := range whList {
				webhooks = append(webhooks, db.WebhookRequest{
					URL:        wh.URL,
					Secret:     wh.Secret,
					TimeoutSec: wh.TimeoutSec,
					MaxRetries: wh.MaxRetries,
				})
			}
		}
	}

	ttlHours := req.TTLHours
	if ttlHours <= 0 {
		ttlHours = h.cfg.Scanner.WatchDefaultTTLHours
	}

	meta := req.Meta
	if meta == nil {
		meta = json.RawMessage("{}")
	}

	cacheReq := cache.WatchRequest{
		Network:         req.Network,
		Address:         normalizedAddress,
		Hex40:           hex40,
		TTLHours:        ttlHours,
		DisableWebhooks: disableWebhooks,
		Webhooks:        webhooks,
		Meta:            meta,
	}

	if err := h.watchCache.Add(r.Context(), cacheReq); err != nil {
		slog.Error("add watched address", "network", req.Network, "address", normalizedAddress, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to add address"})
		return
	}

	wa, err := db.GetWatched(r.Context(), h.db, req.Network, normalizedAddress)
	if err != nil || wa == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"network":          req.Network,
			"address":          normalizedAddress,
			"webhooks_count":   len(webhooks),
			"disable_webhooks": disableWebhooks,
		})
		return
	}

	resp := map[string]interface{}{
		"network":          wa.Network,
		"address":          wa.Address,
		"subscribed_at":    wa.SubscribedAt.UTC().Format(time.RFC3339),
		"expires_at":       wa.ExpiresAt.UTC().Format(time.RFC3339),
		"webhooks_count":   len(wa.Webhooks),
		"disable_webhooks": wa.DisableWebhooks,
		"meta":             wa.Meta,
	}
	if wa.Hex40 != "" {
		resp["hex40"] = wa.Hex40
	}
	if req.Sync {
		go h.syncHistoricalTransactions(context.Background(), req.Network, normalizedAddress, wa.ID)
		resp["sync_started"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

// POST /api/unwatch
func (h *Handler) handleUnwatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Network string `json:"network"`
		Address string `json:"address"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
		return
	}

	if req.Address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address is required"})
		return
	}
	if req.Network == "" {
		req.Network = "tron"
	}

	normalizedAddress, _, err := h.normalizeAddress(req.Network, req.Address)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if err := h.watchCache.Remove(r.Context(), req.Network, normalizedAddress); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "address not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"network": req.Network,
		"address": normalizedAddress,
		"status":  "removed",
	})
}

// GET /api/addresses
func (h *Handler) handleListAddresses(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := parseIntParam(q.Get("limit"), 50)
	offset := parseIntParam(q.Get("offset"), 0)
	network := q.Get("network")

	var active *bool
	if v := q.Get("active"); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			active = &b
		}
	}

	addresses, total, err := db.ListWatched(r.Context(), h.db, limit, offset, active, network, "")
	if err != nil {
		slog.Error("list addresses", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	items := make([]map[string]interface{}, 0, len(addresses))
	for _, a := range addresses {
		item := map[string]interface{}{
			"id":               a.ID,
			"network":          a.Network,
			"address":          a.Address,
			"subscribed_at":    a.SubscribedAt.UTC().Format(time.RFC3339),
			"expires_at":       a.ExpiresAt.UTC().Format(time.RFC3339),
			"active":           a.Active,
			"disable_webhooks": a.DisableWebhooks,
			"webhooks_count":   a.WebhooksCount,
			"tx_count":         a.TxCount,
			"meta":             a.Meta,
		}
		if a.LastSeenAt != nil {
			item["last_seen_at"] = a.LastSeenAt.UTC().Format(time.RFC3339)
		} else {
			item["last_seen_at"] = nil
		}
		if a.Hex40 != "" {
			item["hex40"] = a.Hex40
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total": total,
		"items": items,
	})
}

// GET /api/addresses/{address}
func (h *Handler) handleGetAddress(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	network := r.URL.Query().Get("network") // optional: if empty, search across all networks

	if address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address is required"})
		return
	}

	wa, err := db.GetWatched(r.Context(), h.db, network, address)
	if err != nil {
		slog.Error("get address", "network", network, "address", address, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if wa == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "address not found"})
		return
	}

	stats, err := db.GetAddressStats(r.Context(), h.db, wa.ID)
	if err != nil {
		slog.Error("get address stats", "id", wa.ID, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	webhooksResp := make([]map[string]interface{}, 0, len(wa.Webhooks))
	for _, wh := range wa.Webhooks {
		webhooksResp = append(webhooksResp, map[string]interface{}{
			"id":          wh.ID,
			"url":         wh.URL,
			"timeout_sec": wh.TimeoutSec,
			"max_retries": wh.MaxRetries,
			"active":      wh.Active,
			"created_at":  wh.CreatedAt.UTC().Format(time.RFC3339),
		})
	}

	resp := map[string]interface{}{
		"id":               wa.ID,
		"network":          wa.Network,
		"address":          wa.Address,
		"subscribed_at":    wa.SubscribedAt.UTC().Format(time.RFC3339),
		"expires_at":       wa.ExpiresAt.UTC().Format(time.RFC3339),
		"active":           wa.Active,
		"disable_webhooks": wa.DisableWebhooks,
		"meta":             wa.Meta,
		"webhooks":         webhooksResp,
		"stats": map[string]interface{}{
			"tx_count": stats.TxCount,
		},
	}
	if wa.LastSeenAt != nil {
		resp["last_seen_at"] = wa.LastSeenAt.UTC().Format(time.RFC3339)
	} else {
		resp["last_seen_at"] = nil
	}
	if wa.Hex40 != "" {
		resp["hex40"] = wa.Hex40
	}
	if wa.Balance != nil {
		resp["balance"] = *wa.Balance // float64: native-currency amount
	}
	if wa.SyncedAt != nil {
		resp["synced_at"] = wa.SyncedAt.UTC().Format(time.RFC3339)
	}

	writeJSON(w, http.StatusOK, resp)
}

// GET /api/addresses/{address}/transactions
func (h *Handler) handleGetTransactions(w http.ResponseWriter, r *http.Request) {
	address := r.PathValue("address")
	network := r.URL.Query().Get("network") // optional: if empty, search across all networks

	if address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address is required"})
		return
	}

	wa, err := db.GetWatched(r.Context(), h.db, network, address)
	if err != nil {
		slog.Error("get address for tx", "network", network, "address", address, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if wa == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "address not found"})
		return
	}

	q := r.URL.Query()

	if q.Get("sync") == "true" {
		h.syncHistoricalTransactions(r.Context(), network, address, wa.ID)
	}

	perPage := parseIntParam(q.Get("per_page"), 0)
	if perPage == 0 {
		perPage = parseIntParam(q.Get("limit"), 20)
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 200 {
		perPage = 200
	}

	page := parseIntParam(q.Get("page"), 0)
	var offset int
	if page >= 1 {
		offset = (page - 1) * perPage
	} else {
		offset = parseIntParam(q.Get("offset"), 0)
	}

	filter := db.TxFilter{
		Currency:  q.Get("currency"),
		Direction: q.Get("direction"),
		Limit:     perPage,
		Offset:    offset,
		OrderDesc: strings.ToLower(q.Get("sort")) == "desc",
	}

	if from := q.Get("from"); from != "" {
		t, err := time.Parse("2006-01-02", from)
		if err == nil {
			filter.From = &t
		}
	}
	if to := q.Get("to"); to != "" {
		t, err := time.Parse("2006-01-02", to)
		if err == nil {
			t = t.Add(24*time.Hour - time.Second)
			filter.To = &t
		}
	}

	transactions, total, err := db.GetTransactions(r.Context(), h.db, wa.ID, filter)
	if err != nil {
		slog.Error("get transactions", "address", address, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	lastPage := 1
	if total > 0 {
		lastPage = (total + perPage - 1) / perPage
	}

	items := make([]map[string]interface{}, 0, len(transactions))
	for _, tx := range transactions {
		item := map[string]interface{}{
			"id":        tx.ID,
			"network":   tx.Network,
			"txid":      tx.TxID,
			"ts":        tx.Ts.UTC().Format(time.RFC3339),
			"currency":  tx.Currency,
			"from":      tx.FromAddress,
			"to":        tx.ToAddress,
			"amount":    tx.Amount,
			"direction": tx.Direction,
		}
		if tx.BlockNum != nil {
			item["block"] = *tx.BlockNum
		}
		if tx.AmountRaw != nil {
			item["amount_raw"] = *tx.AmountRaw
		}
		if tx.Fee != nil {
			item["fee"] = *tx.Fee
			item["fee_currency"] = tx.FeeCurrency
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total":    total,
		"per_page": perPage,
		"pages":    lastPage,
		"items":    items,
	})
}

// GET /api/webhooks/deliveries
func (h *Handler) handleListDeliveries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	status := strings.ToUpper(q.Get("status"))
	limit := parseIntParam(q.Get("limit"), 50)
	offset := parseIntParam(q.Get("offset"), 0)

	deliveries, total, err := db.GetDeliveries(r.Context(), h.db, status, limit, offset)
	if err != nil {
		slog.Error("list deliveries", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	items := make([]map[string]interface{}, 0, len(deliveries))
	for _, d := range deliveries {
		item := map[string]interface{}{
			"id":           d.ID,
			"event_id":     d.EventID,
			"webhook_name": d.WebhookName,
			"webhook_url":  d.WebhookURL,
			"status":       d.Status,
			"attempts":     d.Attempts,
		}
		if d.LastAttemptAt != nil {
			item["last_attempt_at"] = d.LastAttemptAt.UTC().Format(time.RFC3339)
		} else {
			item["last_attempt_at"] = nil
		}
		if d.LastStatusCode != nil {
			item["last_status_code"] = *d.LastStatusCode
		} else {
			item["last_status_code"] = nil
		}
		if d.LastError != nil {
			item["last_error"] = *d.LastError
		} else {
			item["last_error"] = nil
		}
		items = append(items, item)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"total": total,
		"items": items,
	})
}

// POST /api/webhooks/deliveries/{id}/retry
func (h *Handler) handleRetryDelivery(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid delivery ID"})
		return
	}

	if err := db.RetryDelivery(r.Context(), h.db, id); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "delivery not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":     id,
		"status": "PENDING",
	})
}

// GET /health
func (h *Handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	mysqlStatus := "ok"
	if err := h.db.PingContext(ctx); err != nil {
		mysqlStatus = fmt.Sprintf("error: %v", err)
	}

	redisStatus := "ok"
	if h.rdb != nil {
		if err := h.rdb.Ping(ctx).Err(); err != nil {
			redisStatus = fmt.Sprintf("error: %v", err)
		}
	} else {
		redisStatus = "disabled"
	}

	lastBlock, _ := db.GetLastBlock(ctx, h.db)
	latestChainBlock, _ := h.tronClient.GetLatestBlockNumber(ctx)

	lagBlocks := latestChainBlock - lastBlock
	if lagBlocks < 0 {
		lagBlocks = 0
	}

	watchedActive := h.watchCache.ActiveCount()
	pendingWebhooks, failedWebhooks, _ := db.CountDeliveriesByStatus(ctx, h.db)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":             "ok",
		"last_block":         lastBlock,
		"latest_chain_block": latestChainBlock,
		"lag_blocks":         lagBlocks,
		"watched_active":     watchedActive,
		"webhooks_pending":   pendingWebhooks,
		"webhooks_failed":    failedWebhooks,
		"redis":              redisStatus,
		"mysql":              mysqlStatus,
	})
}

// GET /api/transactions?network=...&address=...&limit=...
//
// network=trc20  → TronScan API (TRC20 token transfers for the address)
// other networks → Blockchair API (must be listed in config.blockchains with enabled=true)
func (h *Handler) handleFetchTransactions(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	network := q.Get("network")
	address := q.Get("address")
	limit := parseIntParam(q.Get("limit"), 20)
	if limit < 1 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}

	if address == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "address is required"})
		return
	}
	if network == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "network is required"})
		return
	}

	var txs []blockchair.LookupTx

	switch network {
	case "trc20":
		if h.tronscanClient == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "tronscan client not available"})
			return
		}
		transfers, err := h.tronscanClient.GetTRC20Transfers(r.Context(), address, limit)
		if err != nil {
			slog.Error("fetch-transactions: tronscan error", "address", address, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("tronscan request failed: %v", err)})
			return
		}
		txs = formatTronScanTransfers(address, transfers)

	default:
		bc := h.cfg.BlockchainByID(network)
		if bc == nil || !bc.Enabled {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unsupported network: %s", network)})
			return
		}
		if h.blockchairClient == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "blockchair API not configured"})
			return
		}
		dashboard, err := h.blockchairClient.GetAddressDashboard(r.Context(), network, address)
		if err != nil {
			slog.Error("fetch-transactions: blockchair error", "network", network, "address", address, "err", err)
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": fmt.Sprintf("blockchair request failed: %v", err)})
			return
		}
		txs = blockchair.FormatDashboard(network, address, bc, dashboard)

		// Fetch ERC-20 token transactions for each configured token (e.g. USDT, USDC).
		for _, token := range bc.ERC20Tokens {
			erc20Txs, err := h.blockchairClient.GetERC20Transactions(r.Context(), token.Contract, address)
			if err != nil {
				slog.Warn("fetch-transactions: erc20 error", "token", token.Symbol, "address", address, "err", err)
				continue
			}
			txs = append(txs, blockchair.FormatERC20Transactions(address, erc20Txs)...)
		}

		if limit > 0 && len(txs) > limit {
			txs = txs[:limit]
		}
	}

	if txs == nil {
		txs = []blockchair.LookupTx{}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"network":      network,
		"address":      address,
		"total":        len(txs),
		"transactions": txs,
	})
}

// formatTronScanTransfers converts TronScan token transfers into the unified LookupTx format.
func formatTronScanTransfers(address string, transfers []tronscan.TokenTransfer) []blockchair.LookupTx {
	result := make([]blockchair.LookupTx, 0, len(transfers))
	for _, t := range transfers {
		if t.TransactionID == "" {
			continue
		}

		from := strings.ToLower(t.FromAddress)
		to := strings.ToLower(t.ToAddress)

		direction := "OUT"
		if strings.EqualFold(t.ToAddress, address) {
			direction = "IN"
		}

		decimals := t.TokenInfo.Decimals
		if decimals <= 0 {
			decimals = 6
		}

		var rawAmount float64
		fmt.Sscanf(t.Quant, "%f", &rawAmount)
		amount := rawAmount / math.Pow10(decimals)
		amountStr := trimAmount(fmt.Sprintf("%.8f", amount))

		currency := t.TokenInfo.Symbol
		if currency == "" {
			currency = "USDT"
		}

		ts := ""
		if t.BlockTs > 0 {
			ts = time.UnixMilli(t.BlockTs).UTC().Format(time.RFC3339)
		}

		result = append(result, blockchair.LookupTx{
			TxID:      t.TransactionID,
			Ts:        ts,
			Currency:  currency,
			From:      from,
			To:        to,
			Amount:    amountStr,
			Direction: direction,
		})
	}
	return result
}

// trimAmount removes trailing zeros after decimal point.
func trimAmount(s string) string {
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// normalizeAddress validates and normalizes an address for the given network.
// Returns (canonicalAddress, hex40ForTRON, error).
func (h *Handler) normalizeAddress(network, address string) (string, string, error) {
	switch network {
	case "tron":
		if !tron.ValidateAddress(address) {
			return "", "", fmt.Errorf("invalid TRON address")
		}
		hex40, err := tron.Base58ToHex40(address)
		if err != nil {
			return "", "", fmt.Errorf("invalid TRON address: %w", err)
		}
		return address, hex40, nil
	default:
		if address == "" {
			return "", "", fmt.Errorf("address is required")
		}
		// For EVM and other chains: lowercase the address
		return strings.ToLower(address), "", nil
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "err", err)
	}
}

func parseIntParam(s string, defaultVal int) int {
	if s == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return defaultVal
	}
	return n
}
