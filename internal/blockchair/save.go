package blockchair

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/config"
	"github.com/stefan-che/cleanroom-watcher/internal/db"
)

// SaveERC20Transactions fetches and inserts ERC-20 token transfers for one token contract.
// No webhook deliveries are created — intended for initial historical sync.
// Returns the number of newly inserted events.
func SaveERC20Transactions(ctx context.Context, sqlDB *sql.DB, client *Client, watchedID int, network, address string, token config.ERC20Token) (int, error) {
	txs, err := client.GetERC20Transactions(ctx, token.Contract, address)
	if err != nil {
		return 0, err
	}

	decimals := token.Decimals
	if decimals == 0 {
		decimals = 6
	}
	divisor := pow10(decimals)

	saved := 0
	for _, tx := range txs {
		if tx.TransactionHash == "" || tx.Value == "0" || tx.Value == "" {
			continue
		}

		from := strings.ToLower(tx.Sender)
		to := strings.ToLower(tx.Recipient)
		direction := resolveDirection(from, to, address)

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

		id, err := db.InsertEvent(ctx, sqlDB, db.TransferEvent{
			Network:          network,
			TxID:             tx.TransactionHash,
			LogIndex:         int(tx.ID),
			Ts:               ts,
			Currency:         currency,
			FromAddress:      from,
			ToAddress:        to,
			Amount:           amountStr,
			Direction:        direction,
			WatchedAddressID: &watchedID,
		})
		if err != nil {
			slog.Error("sync: insert erc20 tx", "txid", tx.TransactionHash, "token", token.Symbol, "err", err)
			continue
		}
		if id > 0 {
			saved++
		}
	}

	return saved, nil
}

// SaveDashboard inserts all transactions from a Blockchair dashboard as TransferEvents.
// No webhook deliveries are created — intended for initial historical sync.
// Returns the number of newly inserted events.
func SaveDashboard(ctx context.Context, sqlDB *sql.DB, watchedID int, network, address string, bc *config.BlockchainConfig, dashboard *AddressDashboard) (int, error) {
	if dashboard == nil {
		return 0, nil
	}

	decimals := 1e8
	nativeCurrency := strings.ToUpper(network)
	if bc != nil {
		if bc.Decimals > 0 {
			decimals = bc.Decimals
		}
		if bc.NativeCurrency != "" {
			nativeCurrency = bc.NativeCurrency
		}
	} else if network == "tron" {
		decimals = 1e6
		nativeCurrency = "TRX"
	}

	saved := 0

	// ETH internal calls: value in wei, divide by bc.Decimals (1e18).
	for i, call := range dashboard.Calls {
		if !call.Transferred || call.TransactionHash == "" {
			continue
		}

		from := strings.ToLower(call.Sender)
		to := strings.ToLower(call.Recipient)
		direction := resolveDirection(from, to, address)

		amountStr := trimTrailingZeros(fmt.Sprintf("%.8f", call.Value/decimals))

		ts, err := parseTime(call.Time)
		if err != nil {
			ts = time.Now().UTC()
		}

		id, err := db.InsertEvent(ctx, sqlDB, db.TransferEvent{
			Network:          network,
			TxID:             call.TransactionHash,
			LogIndex:         i,
			Ts:               ts,
			Currency:         nativeCurrency,
			FromAddress:      from,
			ToAddress:        to,
			Amount:           amountStr,
			Direction:        direction,
			WatchedAddressID: &watchedID,
		})
		if err != nil {
			slog.Error("sync: insert eth call", "txid", call.TransactionHash, "err", err)
			continue
		}
		if id > 0 {
			saved++
		}
	}

	return saved, nil
}
