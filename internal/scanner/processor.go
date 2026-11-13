package scanner

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/cache"
	"github.com/stefan-che/cleanroom-watcher/internal/config"
	"github.com/stefan-che/cleanroom-watcher/internal/db"
	"github.com/stefan-che/cleanroom-watcher/internal/tron"
	"github.com/stefan-che/cleanroom-watcher/internal/webhook"
)

// TransferTopic is the keccak256 hash of Transfer(address,address,uint256).
const TransferTopic = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

type transfer struct {
	TxID     string
	LogIndex int
	Token    string
	From     string // hex40 lowercase
	To       string // hex40 lowercase
	Amount   *big.Int
	BlockNum int64
	BlockTs  time.Time
}

// Processor handles block processing logic.
type Processor struct {
	tronClient tron.TronClient
	watchCache *cache.WatchCache
	db         *sql.DB
	dispatcher *webhook.Dispatcher
	cfg        *config.Config
}

func NewProcessor(
	tronClient tron.TronClient,
	watchCache *cache.WatchCache,
	db *sql.DB,
	dispatcher *webhook.Dispatcher,
	cfg *config.Config,
) *Processor {
	return &Processor{
		tronClient: tronClient,
		watchCache: watchCache,
		db:         db,
		dispatcher: dispatcher,
		cfg:        cfg,
	}
}

func (p *Processor) processBlock(ctx context.Context, blockNum int64) error {
	start := time.Now()

	block, err := p.tronClient.GetBlock(ctx, blockNum)
	if err != nil {
		return fmt.Errorf("get block %d: %w", blockNum, err)
	}

	if len(block.Transactions) == 0 {
		return nil
	}

	blockTs := time.Unix(block.BlockHeader.RawData.Timestamp/1000, 0).UTC()

	// Fetch all transaction infos in one RPC call instead of one per transaction.
	txInfos, err := p.tronClient.GetTransactionInfosByBlock(ctx, blockNum)
	if err != nil {
		return fmt.Errorf("get tx infos block %d: %w", blockNum, err)
	}
	infoByID := make(map[string]*tron.TransactionInfo, len(txInfos))
	for i := range txInfos {
		if txInfos[i].ID != "" {
			infoByID[txInfos[i].ID] = &txInfos[i]
		}
	}

	var allTransfers []transfer
	for _, tx := range block.Transactions {
		if len(tx.Ret) > 0 && tx.Ret[0].ContractRet != "" && tx.Ret[0].ContractRet != "SUCCESS" {
			continue
		}
		transfers := p.processTransaction(tx, infoByID[tx.TxID], blockNum, blockTs)
		allTransfers = append(allTransfers, transfers...)
	}

	elapsed := time.Since(start)
	slog.Debug("block fetched",
		"block", blockNum,
		"txs", len(block.Transactions),
		"transfers", len(allTransfers),
		"elapsed_ms", elapsed.Milliseconds(),
	)

	if len(allTransfers) == 0 {
		return nil
	}

	allHex40 := make([]string, 0, len(allTransfers)*2)
	seen := make(map[string]struct{})
	for _, t := range allTransfers {
		if _, ok := seen[t.From]; !ok {
			allHex40 = append(allHex40, t.From)
			seen[t.From] = struct{}{}
		}
		if _, ok := seen[t.To]; !ok {
			allHex40 = append(allHex40, t.To)
			seen[t.To] = struct{}{}
		}
	}

	hits := p.watchCache.ContainsAny(allHex40)
	if len(hits) == 0 {
		return nil
	}

	for _, t := range allTransfers {
		fromEntry, fromHit := hits[t.From]
		toEntry, toHit := hits[t.To]

		if !fromHit && !toHit {
			continue
		}

		fromB58, _ := tron.Hex40ToBase58(t.From)
		toB58, _ := tron.Hex40ToBase58(t.To)

		amountRaw := t.Amount.String()
		amountDecimal := formatAmount(t.Amount)

		blockNum := t.BlockNum

		// When both addresses are watched, record two separate events
		type hit struct {
			entry     cache.CacheEntry
			hex40     string
			direction string
			logIndex  int
		}
		var hitsToProcess []hit

		if fromHit {
			hitsToProcess = append(hitsToProcess, hit{fromEntry, t.From, "OUT", t.LogIndex})
		}
		if toHit {
			// If both sides are watched, give the IN event a shifted log_index
			// so the unique key (network, txid, log_index) doesn't collide
			logIdx := t.LogIndex
			if fromHit {
				logIdx = t.LogIndex + 10000
			}
			hitsToProcess = append(hitsToProcess, hit{toEntry, t.To, "IN", logIdx})
		}

		for _, h := range hitsToProcess {
			event := db.TransferEvent{
				Network:          "tron",
				TxID:             t.TxID,
				LogIndex:         h.logIndex,
				BlockNum:         &blockNum,
				Ts:               t.BlockTs,
				Currency:         t.Token,
				FromAddress:      fromB58,
				ToAddress:        toB58,
				FromHex40:        t.From,
				ToHex40:          t.To,
				AmountRaw:        &amountRaw,
				Amount:           amountDecimal,
				Direction:        h.direction,
				WatchedAddressID: &h.entry.AddressID,
			}

			eventID, err := db.InsertEvent(ctx, p.db, event)
			if err != nil {
				slog.Error("insert event", "txid", t.TxID, "err", err)
				continue
			}
			if eventID == 0 {
				continue
			}

			if t.Token != "TRX" {
				if delta, err := strconv.ParseFloat(amountDecimal, 64); err == nil {
					if h.direction == "OUT" {
						delta = -delta
					}
					if err := db.AdjustWatchedBalance(ctx, p.db, h.entry.AddressID, delta); err != nil {
						slog.Error("adjust balance", "watched_id", h.entry.AddressID, "err", err)
					}
				}
			}

			deliveries, err := p.buildDeliveries(ctx, h.entry.AddressID)
			if err != nil {
				slog.Error("build deliveries", "watched_id", h.entry.AddressID, "err", err)
				continue
			}

			if len(deliveries) > 0 {
				dbDeliveries := make([]db.DeliveryRecord, len(deliveries))
				for i, d := range deliveries {
					dbDeliveries[i] = d.DeliveryRecord
				}
				if err := db.CreateWebhookDeliveries(ctx, p.db, eventID, dbDeliveries); err != nil {
					slog.Error("create webhook deliveries", "event_id", eventID, "err", err)
					continue
				}

				watchedB58, _ := tron.Hex40ToBase58(h.hex40)
				payload := webhook.EventPayload{
					Network:   "tron",
					Currency:  t.Token,
					TxID:      t.TxID,
					Block:     t.BlockNum,
					Ts:        t.BlockTs.UTC().Format(time.RFC3339),
					Address:   watchedB58,
					From:      fromB58,
					To:        toB58,
					Amount:    amountDecimal,
					AmountRaw: amountRaw,
					Direction: h.direction,
				}

				pendingDeliveries, err := p.getPendingDeliveriesForEvent(ctx, eventID)
				if err != nil {
					slog.Error("get pending deliveries for event", "event_id", eventID, "err", err)
				} else {
					for i, d := range pendingDeliveries {
						if i < len(deliveries) {
							p.dispatcher.Dispatch(webhook.DeliveryJob{
								DeliveryID: d.ID,
								EventID:    eventID,
								Payload:    payload,
								WebhookURL: d.WebhookURL,
								Secret:     deliveries[i].Secret,
								TimeoutSec: deliveries[i].TimeoutSec,
								MaxRetries: deliveries[i].MaxRetries,
							})
						}
					}
				}
			}

			p.watchCache.TouchActivity(h.hex40, t.BlockTs)
		}
	}

	slog.Info("block processed",
		"block", blockNum,
		"txs", len(block.Transactions),
		"hits", len(hits),
		"elapsed_ms", time.Since(start).Milliseconds(),
	)

	return nil
}

type deliveryInfo struct {
	db.DeliveryRecord
	Secret     string
	TimeoutSec int
	MaxRetries int
}

func (p *Processor) buildDeliveries(ctx context.Context, watchedAddressID int) ([]deliveryInfo, error) {
	// Check if webhooks are disabled for this address
	wa, err := db.GetWatchedByID(ctx, p.db, watchedAddressID)
	if err != nil {
		return nil, err
	}
	if wa != nil && wa.DisableWebhooks {
		return nil, nil
	}

	webhooks, err := db.GetAddressWebhooks(ctx, p.db, watchedAddressID)
	if err != nil {
		return nil, err
	}

	result := make([]deliveryInfo, 0, len(webhooks))
	for _, wh := range webhooks {
		if !wh.Active {
			continue
		}
		result = append(result, deliveryInfo{
			DeliveryRecord: db.DeliveryRecord{
				WebhookName: fmt.Sprintf("address-%d-wh-%d", watchedAddressID, wh.ID),
				WebhookURL:  wh.URL,
			},
			Secret:     wh.Secret,
			TimeoutSec: wh.TimeoutSec,
			MaxRetries: wh.MaxRetries,
		})
	}
	return result, nil
}

func (p *Processor) getPendingDeliveriesForEvent(ctx context.Context, eventID int64) ([]db.WebhookDelivery, error) {
	rows, err := p.db.QueryContext(ctx,
		`SELECT id, event_id, webhook_name, webhook_url, status,
		        attempts, last_attempt_at, next_retry_at,
		        last_status_code, last_error, created_at
		 FROM webhook_deliveries
		 WHERE event_id = ? AND status = 'PENDING' AND (next_retry_at IS NULL OR next_retry_at <= NOW())
		 ORDER BY id ASC`,
		eventID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []db.WebhookDelivery
	for rows.Next() {
		var d db.WebhookDelivery
		if err := rows.Scan(
			&d.ID, &d.EventID, &d.WebhookName, &d.WebhookURL, &d.Status,
			&d.Attempts, &d.LastAttemptAt, &d.NextRetryAt,
			&d.LastStatusCode, &d.LastError, &d.CreatedAt,
		); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

func (p *Processor) processTransaction(tx tron.Transaction, info *tron.TransactionInfo, blockNum int64, blockTs time.Time) []transfer {
	minTRXSun := int64(p.cfg.Scanner.MinTRXAmount * 1_000_000)
	result := parseTRXTransfers(tx, blockNum, blockTs, minTRXSun)
	if info != nil {
		result = append(result, p.parseUSDTTransfers(info, tx.TxID, blockNum, blockTs)...)
	}
	return result
}

func (p *Processor) parseUSDTTransfers(info *tron.TransactionInfo, txID string, blockNum int64, blockTs time.Time) []transfer {
	var result []transfer

	usdtContractHex := ""
	if p.cfg.USDTContract != "" {
		h, err := tron.Base58ToHex40(p.cfg.USDTContract)
		if err == nil {
			usdtContractHex = strings.ToLower(h)
		}
	}

	var minUSDTSun *big.Int
	if p.cfg.Scanner.MinUSDTAmount > 0 {
		minUSDTSun = big.NewInt(int64(p.cfg.Scanner.MinUSDTAmount * 1_000_000))
	}

	for i, log := range info.Log {
		if len(log.Topics) < 3 {
			continue
		}
		if strings.ToLower(log.Topics[0]) != TransferTopic {
			continue
		}

		if usdtContractHex != "" {
			if strings.ToLower(log.Address) != usdtContractHex {
				continue
			}
		}

		if len(log.Topics[1]) < 40 || len(log.Topics[2]) < 40 {
			continue
		}
		from := strings.ToLower(log.Topics[1][len(log.Topics[1])-40:])
		to := strings.ToLower(log.Topics[2][len(log.Topics[2])-40:])

		dataHex := strings.TrimPrefix(log.Data, "0x")
		dataBytes, err := hex.DecodeString(dataHex)
		if err != nil || len(dataBytes) == 0 {
			continue
		}
		amount := new(big.Int).SetBytes(dataBytes)

		if minUSDTSun != nil && amount.Cmp(minUSDTSun) < 0 {
			continue
		}

		result = append(result, transfer{
			TxID:     txID,
			LogIndex: i,
			Token:    "USDT",
			From:     from,
			To:       to,
			Amount:   amount,
			BlockNum: blockNum,
			BlockTs:  blockTs,
		})
	}

	return result
}

func parseTRXTransfers(tx tron.Transaction, blockNum int64, blockTs time.Time, minSun int64) []transfer {
	var result []transfer

	for _, contract := range tx.RawData.Contract {
		if contract.Type != "TransferContract" {
			continue
		}

		v := contract.Parameter.Value
		if v.OwnerAddress == "" || v.ToAddress == "" {
			continue
		}

		if minSun > 0 && v.Amount < minSun {
			continue
		}

		from := strings.ToLower(v.OwnerAddress)
		if len(from) >= 2 {
			from = from[2:]
		}

		to := strings.ToLower(v.ToAddress)
		if len(to) >= 2 {
			to = to[2:]
		}

		result = append(result, transfer{
			TxID:     tx.TxID,
			LogIndex: 0,
			Token:    "TRX",
			From:     from,
			To:       to,
			Amount:   big.NewInt(v.Amount),
			BlockNum: blockNum,
			BlockTs:  blockTs,
		})
	}

	return result
}

func formatAmount(amount *big.Int) string {
	divisor := new(big.Int).SetInt64(1_000_000)
	quotient := new(big.Int)
	remainder := new(big.Int)
	quotient.DivMod(amount, divisor, remainder)
	if remainder.Sign() < 0 {
		remainder.Neg(remainder)
	}
	return fmt.Sprintf("%s.%06d", quotient.String(), remainder.Int64())
}
