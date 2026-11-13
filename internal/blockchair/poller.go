package blockchair

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/config"
	"github.com/stefan-che/cleanroom-watcher/internal/db"
	"github.com/stefan-che/cleanroom-watcher/internal/webhook"
)

// Poller runs one goroutine per enabled blockchair blockchain,
// each with its own poll interval.
type Poller struct {
	client     *Client
	sqlDB      *sql.DB
	cfg        *config.Config
	dispatcher *webhook.Dispatcher
}

// NewPoller creates a new Blockchair Poller.
func NewPoller(sqlDB *sql.DB, cfg *config.Config, dispatcher *webhook.Dispatcher) *Poller {
	return &Poller{
		client:     NewClient(cfg.Blockchair.APIKey),
		sqlDB:      sqlDB,
		cfg:        cfg,
		dispatcher: dispatcher,
	}
}

// Run starts one goroutine per enabled blockchair blockchain and blocks until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	var wg sync.WaitGroup

	for _, bc := range p.cfg.Blockchains {
		if bc.Source != "blockchair" || !bc.Enabled {
			continue
		}

		bcCopy := bc
		interval := time.Duration(p.cfg.EffectivePollInterval(bcCopy)) * time.Second
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.runChain(ctx, bcCopy, interval)
		}()

		slog.Info("blockchair chain poller started", "network", bcCopy.ID, "interval", interval)
	}

	wg.Wait()
	slog.Info("all blockchair pollers stopped")
}

func (p *Poller) runChain(ctx context.Context, bc config.BlockchainConfig, interval time.Duration) {
	// Poll immediately on start, then on ticker
	p.pollChain(ctx, bc)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollChain(ctx, bc)
		}
	}
}

func (p *Poller) pollChain(ctx context.Context, bc config.BlockchainConfig) {
	if !p.isWorkingHour() {
		return
	}

	addresses, err := db.GetAllActiveByNetwork(ctx, p.sqlDB, bc.ID)
	if err != nil {
		slog.Error("blockchair poll: get addresses", "network", bc.ID, "err", err)
		return
	}

	if len(addresses) == 0 {
		return
	}

	slog.Info("blockchair polling chain", "network", bc.ID, "addresses", len(addresses))

	delay := time.Duration(p.cfg.Blockchair.RequestDelayMs) * time.Millisecond

	for _, addr := range addresses {
		if ctx.Err() != nil {
			return
		}
		p.syncAddress(ctx, addr, bc)
		time.Sleep(delay)
	}
}

func (p *Poller) syncAddress(ctx context.Context, addr db.WatchedAddress, bc config.BlockchainConfig) {
	dashboard, err := p.client.GetAddressDashboard(ctx, bc.ID, addr.Address)
	if err != nil {
		slog.Error("blockchair: get dashboard", "network", bc.ID, "address", addr.Address, "err", err)
		return
	}
	if err := db.IncrBlockchairRequestCount(ctx, p.sqlDB, bc.ID, addr.Address); err != nil {
		slog.Warn("blockchair: increment request count", "address", addr.Address, "err", err)
	}
	if dashboard == nil {
		return
	}

	decimals := bc.Decimals
	if decimals == 0 {
		decimals = 1e8
	}

	// Process UTXO transactions (Bitcoin-style: only hashes, need detail fetch).
	if len(dashboard.TxHashes) > 0 {
		p.processUTXOTransactions(ctx, dashboard.TxHashes, addr, bc, decimals)
	}

	// Process internal ETH calls (Ethereum only).
	// Value is in wei — divide by bc.Decimals (1e18) to get ETH.
	// These are native ETH transfers, not ERC-20 token transfers.
	for i, call := range dashboard.Calls {
		if !call.Transferred || call.TransactionHash == "" {
			continue
		}

		from := strings.ToLower(call.Sender)
		to := strings.ToLower(call.Recipient)
		direction := resolveDirection(from, to, addr.Address)

		amount := call.Value / decimals
		amountStr := trimTrailingZeros(fmt.Sprintf("%.8f", amount))

		ts, err := parseTime(call.Time)
		if err != nil {
			ts = time.Now().UTC()
		}

		event := db.TransferEvent{
			Network:          bc.ID,
			TxID:             call.TransactionHash,
			LogIndex:         i,
			Ts:               ts,
			Currency:         bc.NativeCurrency,
			FromAddress:      from,
			ToAddress:        to,
			Amount:           amountStr,
			Direction:        direction,
			WatchedAddressID: &addr.ID,
		}

		p.saveAndDispatch(ctx, event, addr, false)
	}

	// Process ERC-20 token transfers (one request per configured token).
	for _, token := range bc.ERC20Tokens {
		p.syncERC20(ctx, addr, bc, token, decimals)
	}

	if err := db.TouchLastSeen(ctx, p.sqlDB, addr.Network, addr.Address, time.Now().UTC()); err != nil {
		slog.Warn("blockchair: touch last seen", "address", addr.Address, "err", err)
	}
}

// syncERC20 fetches and stores ERC-20 token transfers for one token contract.
func (p *Poller) syncERC20(ctx context.Context, addr db.WatchedAddress, bc config.BlockchainConfig, token config.ERC20Token, _ float64) {
	txs, err := p.client.GetERC20Transactions(ctx, token.Contract, addr.Address)
	if err != nil {
		slog.Error("blockchair: get erc20 transactions",
			"network", bc.ID, "token", token.Symbol, "address", addr.Address, "err", err)
		return
	}
	if err := db.IncrBlockchairRequestCount(ctx, p.sqlDB, bc.ID, addr.Address); err != nil {
		slog.Warn("blockchair: increment request count (erc20)", "address", addr.Address, "err", err)
	}

	decimals := token.Decimals
	if decimals == 0 {
		decimals = 6
	}
	divisor := pow10(decimals)

	for _, tx := range txs {
		if tx.TransactionHash == "" || tx.Value == "0" || tx.Value == "" {
			continue
		}

		from := strings.ToLower(tx.Sender)
		to := strings.ToLower(tx.Recipient)
		direction := resolveDirection(from, to, addr.Address)

		// Use value_approximate (already human-readable) when available,
		// otherwise parse raw value and divide by token decimals.
		var amount float64
		if tx.ValueApprox > 0 {
			amount = tx.ValueApprox
		} else {
			var raw float64
			fmt.Sscanf(tx.Value, "%f", &raw)
			amount = raw / divisor
		}
		amountStr := trimTrailingZeros(fmt.Sprintf("%.8f", amount))

		currency := tx.TokenSymbol
		if currency == "" {
			currency = token.Symbol
		}

		ts, err := parseTime(tx.Time)
		if err != nil {
			ts = time.Now().UTC()
		}

		event := db.TransferEvent{
			Network:          bc.ID,
			TxID:             tx.TransactionHash,
			LogIndex:         int(tx.ID), // unique Blockchair event ID, fits BIGINT
			Ts:               ts,
			Currency:         currency,
			FromAddress:      from,
			ToAddress:        to,
			Amount:           amountStr,
			Direction:        direction,
			WatchedAddressID: &addr.ID,
		}

		p.saveAndDispatch(ctx, event, addr, true)
	}
}

// processUTXOTransactions fetches full transaction details for Bitcoin-style hash-only responses
// and emits IN/OUT events based on inputs/outputs relative to the watched address.
func (p *Poller) processUTXOTransactions(ctx context.Context, hashes []string, addr db.WatchedAddress, bc config.BlockchainConfig, decimals float64) {
	details, err := p.client.GetTransactionDetails(ctx, bc.ID, hashes)
	if err != nil {
		slog.Error("blockchair: get utxo tx details", "network", bc.ID, "address", addr.Address, "err", err)
		return
	}

	watchedLower := strings.ToLower(addr.Address)

	for _, detail := range details {
		if detail == nil {
			continue
		}
		hash := detail.Transaction.Hash
		if hash == "" {
			continue
		}

		ts, err := parseTime(detail.Transaction.Time)
		if err != nil {
			ts = time.Now().UTC()
		}

		feeVal := ParseValue(detail.Transaction.Fee)
		var feeStr string
		if feeVal > 0 {
			feeStr = trimTrailingZeros(fmt.Sprintf("%.8f", feeVal/decimals))
		}

		// Find the primary sender (first input address).
		fromAddr := ""
		for _, inp := range detail.Inputs {
			if a := strings.ToLower(inp.Recipient); a != watchedLower {
				fromAddr = a
				break
			}
		}
		if fromAddr == "" && len(detail.Inputs) > 0 {
			fromAddr = strings.ToLower(detail.Inputs[0].Recipient)
		}

		// Find the primary recipient (first output that is not the watched address, i.e. not change).
		toAddr := ""
		for _, out := range detail.Outputs {
			if a := strings.ToLower(out.Recipient); a != watchedLower {
				toAddr = a
				break
			}
		}

		// Outputs where watched address is the recipient → IN events.
		// log_index range 30000+ to avoid collision with EVM native txs.
		for i, out := range detail.Outputs {
			if strings.ToLower(out.Recipient) != watchedLower {
				continue
			}
			rawAmount := ParseValue(out.Value)
			amountStr := trimTrailingZeros(fmt.Sprintf("%.8f", rawAmount/decimals))
			event := db.TransferEvent{
				Network:          bc.ID,
				TxID:             hash,
				LogIndex:         30000 + i,
				Ts:               ts,
				Currency:         bc.NativeCurrency,
				FromAddress:      fromAddr,
				ToAddress:        watchedLower,
				Amount:           amountStr,
				Direction:        "IN",
				WatchedAddressID: &addr.ID,
			}
			if feeStr != "" {
				event.Fee = &feeStr
				event.FeeCurrency = bc.NativeCurrency
			}
			p.saveAndDispatch(ctx, event, addr, false)
		}

		// Inputs where watched address is the spender → OUT events.
		// log_index range 40000+ to avoid collision.
		for i, inp := range detail.Inputs {
			if strings.ToLower(inp.Recipient) != watchedLower {
				continue
			}
			rawAmount := ParseValue(inp.Value)
			amountStr := trimTrailingZeros(fmt.Sprintf("%.8f", rawAmount/decimals))
			event := db.TransferEvent{
				Network:          bc.ID,
				TxID:             hash,
				LogIndex:         40000 + i,
				Ts:               ts,
				Currency:         bc.NativeCurrency,
				FromAddress:      watchedLower,
				ToAddress:        toAddr,
				Amount:           amountStr,
				Direction:        "OUT",
				WatchedAddressID: &addr.ID,
			}
			if feeStr != "" {
				event.Fee = &feeStr
				event.FeeCurrency = bc.NativeCurrency
			}
			p.saveAndDispatch(ctx, event, addr, false)
		}
	}
}

// isWorkingHour returns true if polling is allowed at the current UTC time.
// When WorkHours.Enabled is false it always returns true.
func (p *Poller) isWorkingHour() bool {
	wh := p.cfg.Blockchair.WorkHours
	if !wh.Enabled {
		return true
	}

	now := time.Now().UTC()

	// Check weekday allowlist.
	if len(wh.WorkDays) > 0 {
		weekday := int(now.Weekday())
		allowed := false
		for _, d := range wh.WorkDays {
			if d == weekday {
				allowed = true
				break
			}
		}
		if !allowed {
			slog.Info("blockchair poll skipped: non-working day", "weekday", now.Weekday().String())
			return false
		}
	}

	// Check time window.
	startH, startM, ok1 := parseHHMM(wh.StartUTC)
	endH, endM, ok2 := parseHHMM(wh.EndUTC)
	if !ok1 || !ok2 {
		// Malformed config — allow rather than silently block.
		slog.Warn("blockchair: invalid work_hours time format, polling allowed", "start", wh.StartUTC, "end", wh.EndUTC)
		return true
	}

	nowMin := now.Hour()*60 + now.Minute()
	startMin := startH*60 + startM
	endMin := endH*60 + endM

	if nowMin < startMin || nowMin >= endMin {
		slog.Info("blockchair poll skipped: outside work hours",
			"current_utc", now.Format("15:04"),
			"window", wh.StartUTC+"-"+wh.EndUTC,
		)
		return false
	}
	return true
}

// parseHHMM parses a "HH:MM" string into hours and minutes.
func parseHHMM(s string) (h, m int, ok bool) {
	var hh, mm int
	if n, _ := fmt.Sscanf(s, "%d:%d", &hh, &mm); n == 2 {
		return hh, mm, true
	}
	return 0, 0, false
}

func (p *Poller) saveAndDispatch(ctx context.Context, event db.TransferEvent, addr db.WatchedAddress, adjustBalance bool) {
	eventID, err := db.InsertEvent(ctx, p.sqlDB, event)
	if err != nil {
		slog.Error("blockchair: insert event", "txid", event.TxID, "err", err)
		return
	}
	if eventID == 0 {
		return
	}

	if adjustBalance {
		if delta, err := strconv.ParseFloat(event.Amount, 64); err == nil {
			if event.Direction == "OUT" {
				delta = -delta
			}
			if err := db.AdjustWatchedBalance(ctx, p.sqlDB, addr.ID, delta); err != nil {
				slog.Error("blockchair: adjust balance", "watched_id", addr.ID, "err", err)
			}
		}
	}

	if addr.DisableWebhooks {
		return
	}

	deliveries, err := p.buildDeliveries(ctx, addr.ID)
	if err != nil {
		slog.Error("blockchair: build deliveries", "watched_id", addr.ID, "err", err)
		return
	}
	if len(deliveries) == 0 {
		return
	}

	dbDeliveries := make([]db.DeliveryRecord, len(deliveries))
	for i, d := range deliveries {
		dbDeliveries[i] = d.record
	}
	if err := db.CreateWebhookDeliveries(ctx, p.sqlDB, eventID, dbDeliveries); err != nil {
		slog.Error("blockchair: create webhook deliveries", "event_id", eventID, "err", err)
		return
	}

	blockNum := int64(0)
	if event.BlockNum != nil {
		blockNum = *event.BlockNum
	}

	payload := webhook.EventPayload{
		Network:   event.Network,
		Currency:  event.Currency,
		TxID:      event.TxID,
		Block:     blockNum,
		Ts:        event.Ts.UTC().Format(time.RFC3339),
		From:      event.FromAddress,
		To:        event.ToAddress,
		Amount:    event.Amount,
		Direction: event.Direction,
	}
	if event.Fee != nil {
		payload.Fee = *event.Fee
		payload.FeeCurrency = event.FeeCurrency
	}

	rows, err := p.sqlDB.QueryContext(ctx,
		`SELECT id, webhook_url FROM webhook_deliveries WHERE event_id = ? AND status = 'PENDING' ORDER BY id ASC`,
		eventID,
	)
	if err != nil {
		slog.Error("blockchair: fetch pending deliveries", "event_id", eventID, "err", err)
		return
	}
	defer rows.Close()

	type pendingRow struct {
		id  int64
		url string
	}
	var pendingList []pendingRow
	for rows.Next() {
		var pd pendingRow
		if err := rows.Scan(&pd.id, &pd.url); err == nil {
			pendingList = append(pendingList, pd)
		}
	}

	for i, pd := range pendingList {
		if i >= len(deliveries) {
			break
		}
		p.dispatcher.Dispatch(webhook.DeliveryJob{
			DeliveryID: pd.id,
			EventID:    eventID,
			Payload:    payload,
			WebhookURL: pd.url,
			Secret:     deliveries[i].secret,
			TimeoutSec: deliveries[i].timeoutSec,
			MaxRetries: deliveries[i].maxRetries,
		})
	}
}

type deliveryInfo struct {
	record     db.DeliveryRecord
	secret     string
	timeoutSec int
	maxRetries int
}

func (p *Poller) buildDeliveries(ctx context.Context, watchedAddressID int) ([]deliveryInfo, error) {
	webhooks, err := db.GetAddressWebhooks(ctx, p.sqlDB, watchedAddressID)
	if err != nil {
		return nil, err
	}

	var result []deliveryInfo
	for _, wh := range webhooks {
		if !wh.Active {
			continue
		}
		result = append(result, deliveryInfo{
			record: db.DeliveryRecord{
				WebhookName: fmt.Sprintf("address-%d-wh-%d", watchedAddressID, wh.ID),
				WebhookURL:  wh.URL,
			},
			secret:     wh.Secret,
			timeoutSec: wh.TimeoutSec,
			maxRetries: wh.MaxRetries,
		})
	}
	return result, nil
}

func resolveDirection(from, to, watchedAddress string) string {
	if strings.ToLower(to) == strings.ToLower(watchedAddress) {
		return "IN"
	}
	return "OUT"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time string")
	}
	t, err := time.Parse("2006-01-02 15:04:05", s)
	if err == nil {
		return t.UTC(), nil
	}
	t, err = time.Parse(time.RFC3339, s)
	if err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("cannot parse time %q", s)
}

func trimTrailingZeros(s string) string {
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

func pow10(n int) float64 {
	r := 1.0
	for i := 0; i < n; i++ {
		r *= 10
	}
	return r
}
