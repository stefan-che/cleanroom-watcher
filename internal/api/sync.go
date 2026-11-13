package api

import (
	"context"
	"log/slog"

	"github.com/stefan-che/cleanroom-watcher/internal/blockchair"
	"github.com/stefan-che/cleanroom-watcher/internal/config"
	"github.com/stefan-che/cleanroom-watcher/internal/db"
)

func (h *Handler) syncHistoricalTransactions(ctx context.Context, network, address string, watchedID int) {
	log := slog.With("op", "initial_sync", "network", network, "address", address)

	if h.blockchairClient == nil {
		log.Warn("blockchair client not configured, skipping initial sync")
		return
	}

	log.Info("starting initial sync")

	bc := h.cfg.BlockchainByID(network)

	// Sync native currency (ETH internal calls / UTXO).
	dashboard, err := h.blockchairClient.GetAddressDashboard(ctx, network, address)
	if err != nil {
		log.Error("get dashboard", "err", err)
		return
	}

	totalSaved := 0
	if dashboard != nil {
		saved, err := blockchair.SaveDashboard(ctx, h.db, watchedID, network, address, bc, dashboard)
		if err != nil {
			log.Error("save dashboard", "err", err)
		} else {
			totalSaved += saved
		}

		balance := normalizeBalance(dashboard.RawBalance, bc, network)
		if err := db.UpdateAddressBalance(ctx, h.db, network, address, balance); err != nil {
			log.Warn("update balance", "err", err)
		}
	}

	// Sync ERC-20 token transactions (USDT, USDC, etc.) configured for this chain.
	if bc != nil {
		for _, token := range bc.ERC20Tokens {
			saved, err := blockchair.SaveERC20Transactions(ctx, h.db, h.blockchairClient, watchedID, network, address, token)
			if err != nil {
				log.Error("save erc20 transactions", "token", token.Symbol, "err", err)
				continue
			}
			log.Info("erc20 sync complete", "token", token.Symbol, "saved", saved)
			totalSaved += saved
		}
	}

	log.Info("initial sync complete", "total_saved", totalSaved)
}

func normalizeBalance(rawBalance float64, bc *config.BlockchainConfig, network string) float64 {
	decimals := 1e8
	if bc != nil && bc.Decimals > 0 {
		decimals = bc.Decimals
	} else if network == "tron" {
		decimals = 1e6
	}
	return rawBalance / decimals
}
