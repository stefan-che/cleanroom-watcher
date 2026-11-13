package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// TransferEvent represents a blockchain transfer event.
type TransferEvent struct {
	ID               int64
	Network          string
	TxID             string
	LogIndex         int
	BlockNum         *int64
	Ts               time.Time
	Currency         string
	FromAddress      string
	ToAddress        string
	FromHex40        string // TRON only
	ToHex40          string // TRON only
	AmountRaw        *string
	Amount           string
	Fee              *string
	FeeCurrency      string
	Direction        string // 'IN' | 'OUT'
	WatchedAddressID *int
}

// DeliveryRecord is the input for creating a webhook delivery record.
type DeliveryRecord struct {
	WebhookName string
	WebhookURL  string
}

// WebhookDelivery represents a webhook delivery record.
type WebhookDelivery struct {
	ID             int64
	EventID        int64
	WebhookName    string
	WebhookURL     string
	Status         string
	Attempts       int
	LastAttemptAt  *time.Time
	NextRetryAt    *time.Time
	LastStatusCode *int
	LastError      *string
	CreatedAt      time.Time
}

// AddressStats holds aggregate statistics for a watched address.
type AddressStats struct {
	TxCount int64
}

// TxFilter holds filtering parameters for transaction queries.
type TxFilter struct {
	Currency  string
	Direction string
	From      *time.Time
	To        *time.Time
	Limit     int
	Offset    int
	OrderDesc bool
	All       bool // when true, ignore Limit/Offset and return every matching row (used for export)
}

// touchMsg is sent over the channel to update last_seen_at.
type touchMsg struct {
	network string
	address string
	ts      time.Time
}

// TouchActivityWorker manages async updates of last_seen_at.
type TouchActivityWorker struct {
	db *sql.DB
	ch chan touchMsg
}

// NewTouchActivityWorker creates a new TouchActivityWorker and starts the background goroutine.
func NewTouchActivityWorker(ctx context.Context, db *sql.DB, bufSize int) *TouchActivityWorker {
	w := &TouchActivityWorker{
		db: db,
		ch: make(chan touchMsg, bufSize),
	}
	go w.run(ctx)
	return w
}

// Touch sends an async touch message.
func (w *TouchActivityWorker) Touch(network, address string, ts time.Time) {
	select {
	case w.ch <- touchMsg{network: network, address: address, ts: ts}:
	default:
	}
}

func (w *TouchActivityWorker) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-w.ch:
			if !ok {
				return
			}
			_, err := w.db.ExecContext(ctx,
				`UPDATE watched_addresses SET last_seen_at = ? WHERE network = ? AND address = ?`,
				msg.ts, msg.network, msg.address,
			)
			if err != nil {
				slog.Error("touch activity", "network", msg.network, "address", msg.address, "err", err)
			}
		}
	}
}

// InsertEvent inserts a transfer event and returns the new event ID.
// Returns 0 if the event already exists (idempotent).
func InsertEvent(ctx context.Context, db *sql.DB, event TransferEvent) (int64, error) {
	var fromHex40, toHex40 *string
	if event.FromHex40 != "" {
		fromHex40 = &event.FromHex40
	}
	if event.ToHex40 != "" {
		toHex40 = &event.ToHex40
	}

	result, err := db.ExecContext(ctx,
		`INSERT IGNORE INTO transfer_events
		   (network, txid, log_index, block_num, ts, currency,
		    from_address, to_address, from_hex40, to_hex40,
		    amount_raw, amount, fee, fee_currency,
		    direction, watched_address_id)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		event.Network, event.TxID, event.LogIndex, event.BlockNum, event.Ts, event.Currency,
		event.FromAddress, event.ToAddress, fromHex40, toHex40,
		event.AmountRaw, event.Amount, event.Fee, nullIfEmpty(event.FeeCurrency),
		event.Direction, event.WatchedAddressID,
	)
	if err != nil {
		return 0, fmt.Errorf("insert event: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return 0, nil
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	return id, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// CreateWebhookDeliveries creates delivery records for a given event.
func CreateWebhookDeliveries(ctx context.Context, db *sql.DB, eventID int64, deliveries []DeliveryRecord) error {
	if len(deliveries) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	for _, d := range deliveries {
		_, err = tx.ExecContext(ctx,
			`INSERT INTO webhook_deliveries (event_id, webhook_name, webhook_url, status)
			 VALUES (?, ?, ?, 'PENDING')`,
			eventID, d.WebhookName, d.WebhookURL,
		)
		if err != nil {
			return fmt.Errorf("insert webhook delivery: %w", err)
		}
	}

	return tx.Commit()
}

// GetAddressStats returns aggregate statistics for a watched address.
func GetAddressStats(ctx context.Context, db *sql.DB, watchedID int) (*AddressStats, error) {
	var stats AddressStats
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) AS tx_count FROM transfer_events WHERE watched_address_id = ?`,
		watchedID,
	).Scan(&stats.TxCount)
	if err != nil {
		return nil, fmt.Errorf("get address stats: %w", err)
	}
	return &stats, nil
}

// GetTransactions returns paginated transfer events for a watched address.
func GetTransactions(ctx context.Context, db *sql.DB, watchedID int, filter TxFilter) ([]TransferEvent, int, error) {
	args := []interface{}{watchedID}
	where := " WHERE watched_address_id = ?"

	if filter.Currency != "" {
		where += " AND currency = ?"
		args = append(args, filter.Currency)
	}
	if filter.Direction != "" {
		where += " AND direction = ?"
		args = append(args, filter.Direction)
	}
	if filter.From != nil {
		where += " AND ts >= ?"
		args = append(args, *filter.From)
	}
	if filter.To != nil {
		where += " AND ts <= ?"
		args = append(args, *filter.To)
	}

	var total int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM transfer_events`+where,
		args...,
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count transactions: %w", err)
	}

	order := " ORDER BY ts ASC"
	if filter.OrderDesc {
		order = " ORDER BY ts DESC"
	}

	const cols = `SELECT id, network, txid, log_index, block_num, ts, currency,
		        from_address, to_address, from_hex40, to_hex40,
		        amount_raw, amount, fee, fee_currency,
		        direction, watched_address_id
		 FROM transfer_events`

	query := cols + where + order
	listArgs := args
	if !filter.All {
		limit := filter.Limit
		if limit <= 0 {
			limit = 20
		}
		query += ` LIMIT ? OFFSET ?`
		listArgs = append(args, limit, filter.Offset)
	}

	rows, err := db.QueryContext(ctx, query, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query transactions: %w", err)
	}
	defer rows.Close()

	var result []TransferEvent
	for rows.Next() {
		var e TransferEvent
		var fromHex40, toHex40, feeCurrency sql.NullString
		if err := rows.Scan(
			&e.ID, &e.Network, &e.TxID, &e.LogIndex, &e.BlockNum, &e.Ts, &e.Currency,
			&e.FromAddress, &e.ToAddress, &fromHex40, &toHex40,
			&e.AmountRaw, &e.Amount, &e.Fee, &feeCurrency,
			&e.Direction, &e.WatchedAddressID,
		); err != nil {
			return nil, 0, fmt.Errorf("scan transaction: %w", err)
		}
		e.FromHex40 = fromHex40.String
		e.ToHex40 = toHex40.String
		e.FeeCurrency = feeCurrency.String
		result = append(result, e)
	}
	return result, total, rows.Err()
}

// GetDeliveries returns paginated webhook deliveries, optionally filtered by status.
func GetDeliveries(ctx context.Context, db *sql.DB, status string, limit, offset int) ([]WebhookDelivery, int, error) {
	args := []interface{}{}
	where := ""
	if status != "" {
		where = " WHERE status = ?"
		args = append(args, status)
	}

	var total int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM webhook_deliveries`+where,
		args...,
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count deliveries: %w", err)
	}

	if limit <= 0 {
		limit = 50
	}
	listArgs := append(args, limit, offset)

	rows, err := db.QueryContext(ctx,
		`SELECT id, event_id, webhook_name, webhook_url, status,
		        attempts, last_attempt_at, next_retry_at,
		        last_status_code, last_error, created_at
		 FROM webhook_deliveries`+where+
			` ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		listArgs...,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("query deliveries: %w", err)
	}
	defer rows.Close()

	var result []WebhookDelivery
	for rows.Next() {
		var d WebhookDelivery
		if err := rows.Scan(
			&d.ID, &d.EventID, &d.WebhookName, &d.WebhookURL, &d.Status,
			&d.Attempts, &d.LastAttemptAt, &d.NextRetryAt,
			&d.LastStatusCode, &d.LastError, &d.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("scan delivery: %w", err)
		}
		result = append(result, d)
	}
	return result, total, rows.Err()
}

// RetryDelivery resets a delivery to PENDING with 0 attempts.
func RetryDelivery(ctx context.Context, db *sql.DB, id int64) error {
	result, err := db.ExecContext(ctx,
		`UPDATE webhook_deliveries
		 SET status = 'PENDING', attempts = 0, next_retry_at = NULL, last_error = NULL
		 WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("retry delivery: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("delivery %d not found", id)
	}
	return nil
}

// GetPendingDeliveries returns deliveries that are ready to be retried.
func GetPendingDeliveries(ctx context.Context, db *sql.DB, limit int) ([]WebhookDelivery, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.QueryContext(ctx,
		`SELECT id, event_id, webhook_name, webhook_url, status,
		        attempts, last_attempt_at, next_retry_at,
		        last_status_code, last_error, created_at
		 FROM webhook_deliveries
		 WHERE status = 'PENDING' AND (next_retry_at IS NULL OR next_retry_at <= NOW())
		 ORDER BY ISNULL(next_retry_at) DESC, next_retry_at ASC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("get pending deliveries: %w", err)
	}
	defer rows.Close()

	var result []WebhookDelivery
	for rows.Next() {
		var d WebhookDelivery
		if err := rows.Scan(
			&d.ID, &d.EventID, &d.WebhookName, &d.WebhookURL, &d.Status,
			&d.Attempts, &d.LastAttemptAt, &d.NextRetryAt,
			&d.LastStatusCode, &d.LastError, &d.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan pending delivery: %w", err)
		}
		result = append(result, d)
	}
	return result, rows.Err()
}

// UpdateDeliveryAttempt updates a delivery after an attempt.
func UpdateDeliveryAttempt(ctx context.Context, db *sql.DB, id int64, success bool, statusCode int, errMsg string, nextRetry *time.Time) error {
	var status string
	if success {
		status = "SENT"
	} else if nextRetry == nil {
		status = "FAILED"
	} else {
		status = "PENDING"
	}

	var errPtr *string
	if errMsg != "" {
		errPtr = &errMsg
	}

	var codePtr *int
	if statusCode != 0 {
		codePtr = &statusCode
	}

	_, err := db.ExecContext(ctx,
		`UPDATE webhook_deliveries
		 SET status = ?,
		     attempts = attempts + 1,
		     last_attempt_at = NOW(),
		     next_retry_at = ?,
		     last_status_code = ?,
		     last_error = ?
		 WHERE id = ?`,
		status, nextRetry, codePtr, errPtr, id,
	)
	if err != nil {
		return fmt.Errorf("update delivery attempt: %w", err)
	}
	return nil
}

// CountDeliveriesByStatus returns counts of PENDING and FAILED deliveries.
func CountDeliveriesByStatus(ctx context.Context, db *sql.DB) (pending, failed int64, err error) {
	rows, err := db.QueryContext(ctx,
		`SELECT status, COUNT(*) FROM webhook_deliveries WHERE status IN ('PENDING','FAILED') GROUP BY status`,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("count deliveries by status: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var st string
		var count int64
		if err := rows.Scan(&st, &count); err != nil {
			return 0, 0, err
		}
		switch st {
		case "PENDING":
			pending = count
		case "FAILED":
			failed = count
		}
	}
	return pending, failed, rows.Err()
}
