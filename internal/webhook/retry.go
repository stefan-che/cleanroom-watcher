package webhook

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/db"
)

const (
	retryInterval  = 5 * time.Second
	retryBatchSize = 100
)

// RetryWorker periodically picks up PENDING deliveries and dispatches them.
type RetryWorker struct {
	db         *sql.DB
	dispatcher *Dispatcher
}

func NewRetryWorker(sqlDB *sql.DB, dispatcher *Dispatcher) *RetryWorker {
	return &RetryWorker{
		db:         sqlDB,
		dispatcher: dispatcher,
	}
}

// Run starts the retry worker loop. Blocks until ctx is cancelled.
func (w *RetryWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(retryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *RetryWorker) tick(ctx context.Context) {
	deliveries, err := db.GetPendingDeliveries(ctx, w.db, retryBatchSize)
	if err != nil {
		slog.Error("retry worker: get pending deliveries", "err", err)
		return
	}

	if len(deliveries) == 0 {
		return
	}

	slog.Debug("retry worker: dispatching", "count", len(deliveries))

	for _, d := range deliveries {
		payload, webhookURL, secret, timeoutSec, maxRetries, err := w.loadDeliveryDetails(ctx, d)
		if err != nil {
			slog.Error("retry worker: load delivery details", "id", d.ID, "err", err)
			continue
		}

		w.dispatcher.Dispatch(DeliveryJob{
			DeliveryID: d.ID,
			EventID:    d.EventID,
			Payload:    payload,
			WebhookURL: webhookURL,
			Secret:     secret,
			TimeoutSec: timeoutSec,
			MaxRetries: maxRetries,
		})
	}
}

func (w *RetryWorker) loadDeliveryDetails(ctx context.Context, d db.WebhookDelivery) (EventPayload, string, string, int, int, error) {
	var (
		network     string
		txid        string
		blockNum    sql.NullInt64
		ts          time.Time
		currency    string
		fromAddress string
		toAddress   string
		amountRaw   sql.NullString
		amount      string
		fee         sql.NullString
		feeCurrency sql.NullString
		direction   string
	)

	err := w.db.QueryRowContext(ctx,
		`SELECT network, txid, block_num, ts, currency,
		        from_address, to_address,
		        amount_raw, amount, fee, fee_currency, direction
		 FROM transfer_events WHERE id = ?`,
		d.EventID,
	).Scan(
		&network, &txid, &blockNum, &ts, &currency,
		&fromAddress, &toAddress,
		&amountRaw, &amount, &fee, &feeCurrency, &direction,
	)
	if err != nil {
		return EventPayload{}, "", "", 0, 0, err
	}

	var secret string
	var timeoutSec, maxRetries int

	err = w.db.QueryRowContext(ctx,
		`SELECT COALESCE(secret, ''), timeout_sec, max_retries
		 FROM address_webhooks WHERE url = ? LIMIT 1`,
		d.WebhookURL,
	).Scan(&secret, &timeoutSec, &maxRetries)
	if err != nil {
		timeoutSec = 10
		maxRetries = 3
	}

	payload := EventPayload{
		Network:   network,
		Currency:  currency,
		TxID:      txid,
		Ts:        ts.UTC().Format(time.RFC3339),
		From:      fromAddress,
		To:        toAddress,
		Amount:    amount,
		Direction: direction,
	}
	if blockNum.Valid {
		payload.Block = blockNum.Int64
	}
	if amountRaw.Valid {
		payload.AmountRaw = amountRaw.String
	}
	if fee.Valid {
		payload.Fee = fee.String
		if feeCurrency.Valid {
			payload.FeeCurrency = feeCurrency.String
		}
	}

	return payload, d.WebhookURL, secret, timeoutSec, maxRetries, nil
}
