package blockchair

import (
	"fmt"
	"strings"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/config"
)

// LookupTx is a formatted transaction from a Blockchair one-shot lookup.
type LookupTx struct {
	TxID        string `json:"txid"`
	Ts          string `json:"ts"`
	Currency    string `json:"currency"`
	From        string `json:"from"`
	To          string `json:"to"`
	Amount      string `json:"amount"`
	Direction   string `json:"direction"`
	Fee         string `json:"fee,omitempty"`
	FeeCurrency string `json:"fee_currency,omitempty"`
}

// FormatDashboard converts a Blockchair AddressDashboard into a slice of LookupTx.
// bc may be nil when the network is not present in config.blockchains (e.g. tron via tron_node);
// sensible defaults are applied in that case.
func FormatDashboard(network, address string, bc *config.BlockchainConfig, dashboard *AddressDashboard) []LookupTx {
	if dashboard == nil {
		return nil
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
	} else {
		// Per-network fallbacks for sources not listed in config.blockchains
		switch network {
		case "tron":
			decimals = 1e6
			nativeCurrency = "TRX"
		}
	}

	var result []LookupTx

	// ETH internal calls: value in wei, divide by bc.Decimals (1e18).
	for _, call := range dashboard.Calls {
		if !call.Transferred || call.TransactionHash == "" {
			continue
		}

		from := strings.ToLower(call.Sender)
		to := strings.ToLower(call.Recipient)
		direction := resolveDirection(from, to, address)

		amountStr := trimTrailingZeros(fmt.Sprintf("%.8f", call.Value/decimals))

		result = append(result, LookupTx{
			TxID:      call.TransactionHash,
			Ts:        parseBCTime(call.Time),
			Currency:  nativeCurrency,
			From:      from,
			To:        to,
			Amount:    amountStr,
			Direction: direction,
		})
	}

	return result
}

// FormatERC20Transactions converts a slice of ERC-20 transactions into LookupTx.
// address is the watched address (used to determine IN/OUT direction).
func FormatERC20Transactions(address string, txs []ERC20Transaction) []LookupTx {
	var result []LookupTx
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
		} else if tx.TokenDecimals > 0 {
			var raw float64
			fmt.Sscanf(tx.Value, "%f", &raw)
			divisor := 1.0
			for i := 0; i < tx.TokenDecimals; i++ {
				divisor *= 10
			}
			amount = raw / divisor
		}
		amountStr := trimTrailingZeros(fmt.Sprintf("%.8f", amount))

		currency := tx.TokenSymbol

		result = append(result, LookupTx{
			TxID:      tx.TransactionHash,
			Ts:        parseBCTime(tx.Time),
			Currency:  currency,
			From:      from,
			To:        to,
			Amount:    amountStr,
			Direction: direction,
		})
	}
	return result
}

// parseBCTime parses a Blockchair timestamp and returns RFC3339 UTC.
// Falls back to the original string if parsing fails.
func parseBCTime(s string) string {
	if s == "" {
		return ""
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC().Format(time.RFC3339)
	}
	return s
}
