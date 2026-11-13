package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// WatchRequest is the input for adding a watched address.
type WatchRequest struct {
	Network         string
	Address         string
	Hex40           string // TRON only, empty for other chains
	TTLHours        int
	DisableWebhooks bool
	Webhooks        []WebhookRequest
	Meta            json.RawMessage
}

// WebhookRequest is the input for adding a webhook to a watched address.
type WebhookRequest struct {
	URL        string
	Secret     string
	TimeoutSec int
	MaxRetries int
}

// WatchedAddress represents a fully loaded watched address record.
type WatchedAddress struct {
	ID              int
	Network         string
	Address         string
	Hex40           string
	SubscribedAt    time.Time
	LastSeenAt      *time.Time
	ExpiresAt       time.Time
	Active          bool
	DisableWebhooks bool
	Meta            json.RawMessage
	WebhooksCount   int
}

// WatchedAddressDetail is a watched address with full webhook list.
type WatchedAddressDetail struct {
	ID              int
	Network         string
	Address         string
	Hex40           string
	SubscribedAt    time.Time
	LastSeenAt      *time.Time
	ExpiresAt       time.Time
	Active          bool
	DisableWebhooks bool
	Meta            json.RawMessage
	Webhooks        []AddressWebhook
	Balance         *float64   // token balance (USDT/USDC/etc.), updated on each token transfer
	SyncedAt        *time.Time // timestamp of last initial sync
}

// WatchedAddressSummary is a lightweight summary for list queries.
type WatchedAddressSummary struct {
	ID              int
	Network         string
	Address         string
	Hex40           string
	SubscribedAt    time.Time
	LastSeenAt      *time.Time
	ExpiresAt       time.Time
	Active          bool
	DisableWebhooks bool
	Meta            json.RawMessage
	WebhooksCount   int
	TxCount         int64
}

// AddressWebhook represents a per-address webhook configuration.
type AddressWebhook struct {
	ID               int
	WatchedAddressID int
	URL              string
	Secret           string
	TimeoutSec       int
	MaxRetries       int
	Active           bool
	CreatedAt        time.Time
}

// AddWatched inserts or updates a watched address and its webhooks.
func AddWatched(ctx context.Context, db *sql.DB, req WatchRequest) (*WatchedAddress, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	ttl := req.TTLHours
	if ttl <= 0 {
		ttl = 72
	}
	expiresAt := time.Now().UTC().Add(time.Duration(ttl) * time.Hour)

	meta := req.Meta
	if meta == nil {
		meta = json.RawMessage("{}")
	}

	var hex40Val *string
	if req.Hex40 != "" {
		hex40Val = &req.Hex40
	}

	disableWebhooks := 0
	if req.DisableWebhooks {
		disableWebhooks = 1
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO watched_addresses (network, address, hex40, expires_at, active, disable_webhooks, meta)
		 VALUES (?, ?, ?, ?, 1, ?, ?)
		 ON DUPLICATE KEY UPDATE
		   hex40            = VALUES(hex40),
		   expires_at       = VALUES(expires_at),
		   active           = 1,
		   disable_webhooks = VALUES(disable_webhooks),
		   meta             = VALUES(meta)`,
		req.Network, req.Address, hex40Val, expiresAt, disableWebhooks, string(meta),
	)
	if err != nil {
		return nil, fmt.Errorf("upsert watched address: %w", err)
	}

	var id int64
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM watched_addresses WHERE network = ? AND address = ?`,
		req.Network, req.Address,
	).Scan(&id); err != nil {
		return nil, fmt.Errorf("fetch watched address id: %w", err)
	}

	_, err = tx.ExecContext(ctx,
		`DELETE FROM address_webhooks WHERE watched_address_id = ?`, id,
	)
	if err != nil {
		return nil, fmt.Errorf("delete old webhooks: %w", err)
	}

	for _, wh := range req.Webhooks {
		timeoutSec := wh.TimeoutSec
		if timeoutSec <= 0 {
			timeoutSec = 10
		}
		maxRetries := wh.MaxRetries
		if maxRetries <= 0 {
			maxRetries = 3
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO address_webhooks (watched_address_id, url, secret, timeout_sec, max_retries, active)
			 VALUES (?, ?, ?, ?, ?, 1)`,
			id, wh.URL, wh.Secret, timeoutSec, maxRetries,
		)
		if err != nil {
			return nil, fmt.Errorf("insert webhook: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit tx: %w", err)
	}

	var wa WatchedAddress
	var metaBytes []byte
	var hex40Scan sql.NullString
	var disableWH int
	err = db.QueryRowContext(ctx,
		`SELECT id, network, address, hex40, subscribed_at, last_seen_at, expires_at, active, disable_webhooks, meta
		 FROM watched_addresses WHERE id = ?`, id,
	).Scan(
		&wa.ID, &wa.Network, &wa.Address, &hex40Scan,
		&wa.SubscribedAt, &wa.LastSeenAt, &wa.ExpiresAt, &wa.Active,
		&disableWH, &metaBytes,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch watched address: %w", err)
	}
	wa.Hex40 = hex40Scan.String
	wa.DisableWebhooks = disableWH != 0
	wa.Meta = metaBytes
	wa.WebhooksCount = len(req.Webhooks)

	return &wa, nil
}

// SetWatchedActive activates or deactivates a watched address.
// On activation the expiry is extended by 7 days from now.
func SetWatchedActive(ctx context.Context, db *sql.DB, network, address string, active bool) error {
	var err error
	if active {
		_, err = db.ExecContext(ctx,
			`UPDATE watched_addresses SET active = 1, expires_at = DATE_ADD(NOW(), INTERVAL 7 DAY) WHERE network = ? AND address = ?`,
			network, address,
		)
	} else {
		_, err = db.ExecContext(ctx,
			`UPDATE watched_addresses SET active = 0 WHERE network = ? AND address = ?`,
			network, address,
		)
	}
	if err != nil {
		return fmt.Errorf("set watched active: %w", err)
	}
	return nil
}

// RemoveWatched marks an address as inactive.
func RemoveWatched(ctx context.Context, db *sql.DB, network, address string) error {
	result, err := db.ExecContext(ctx,
		`UPDATE watched_addresses SET active = 0 WHERE network = ? AND address = ?`,
		network, address,
	)
	if err != nil {
		return fmt.Errorf("remove watched: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteWatched permanently removes a watched address and all its transactions from the database.
func DeleteWatched(ctx context.Context, db *sql.DB, network, address string) error {
	result, err := db.ExecContext(ctx,
		`DELETE FROM watched_addresses WHERE network = ? AND address = ?`,
		network, address,
	)
	if err != nil {
		return fmt.Errorf("delete watched: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// GetAllActive returns all active and non-expired watched addresses.
func GetAllActive(ctx context.Context, db *sql.DB) ([]WatchedAddress, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, network, address, hex40, subscribed_at, last_seen_at, expires_at, active, disable_webhooks, meta
		 FROM watched_addresses
		 WHERE active = 1 AND expires_at > NOW()`,
	)
	if err != nil {
		return nil, fmt.Errorf("query active addresses: %w", err)
	}
	defer rows.Close()

	var result []WatchedAddress
	for rows.Next() {
		var wa WatchedAddress
		var metaBytes []byte
		var hex40Scan sql.NullString
		var disableWH int
		if err := rows.Scan(
			&wa.ID, &wa.Network, &wa.Address, &hex40Scan,
			&wa.SubscribedAt, &wa.LastSeenAt, &wa.ExpiresAt, &wa.Active,
			&disableWH, &metaBytes,
		); err != nil {
			return nil, fmt.Errorf("scan watched address: %w", err)
		}
		wa.Hex40 = hex40Scan.String
		wa.DisableWebhooks = disableWH != 0
		wa.Meta = metaBytes
		result = append(result, wa)
	}
	return result, rows.Err()
}

// GetAllActiveByNetwork returns active non-expired watched addresses for a specific network.
func GetAllActiveByNetwork(ctx context.Context, db *sql.DB, network string) ([]WatchedAddress, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, network, address, hex40, subscribed_at, last_seen_at, expires_at, active, disable_webhooks, meta
		 FROM watched_addresses
		 WHERE active = 1 AND expires_at > NOW() AND network = ?`,
		network,
	)
	if err != nil {
		return nil, fmt.Errorf("query active addresses by network: %w", err)
	}
	defer rows.Close()

	var result []WatchedAddress
	for rows.Next() {
		var wa WatchedAddress
		var metaBytes []byte
		var hex40Scan sql.NullString
		var disableWH int
		if err := rows.Scan(
			&wa.ID, &wa.Network, &wa.Address, &hex40Scan,
			&wa.SubscribedAt, &wa.LastSeenAt, &wa.ExpiresAt, &wa.Active,
			&disableWH, &metaBytes,
		); err != nil {
			return nil, fmt.Errorf("scan watched address: %w", err)
		}
		wa.Hex40 = hex40Scan.String
		wa.DisableWebhooks = disableWH != 0
		wa.Meta = metaBytes
		result = append(result, wa)
	}
	return result, rows.Err()
}

// GetWatched returns a watched address detail by network + address.
func GetWatched(ctx context.Context, db *sql.DB, network, address string) (*WatchedAddressDetail, error) {
	var wa WatchedAddressDetail
	var metaBytes []byte
	var hex40Scan sql.NullString
	var disableWH int
	var balance sql.NullFloat64
	var syncedAt sql.NullTime

	var query string
	var args []any
	if network == "" {
		query = `SELECT id, network, address, hex40, subscribed_at, last_seen_at, expires_at, active, disable_webhooks, meta, balance, synced_at
		         FROM watched_addresses WHERE address = ? ORDER BY subscribed_at DESC LIMIT 1`
		args = []any{address}
	} else {
		query = `SELECT id, network, address, hex40, subscribed_at, last_seen_at, expires_at, active, disable_webhooks, meta, balance, synced_at
		         FROM watched_addresses WHERE network = ? AND address = ?`
		args = []any{network, address}
	}

	err := db.QueryRowContext(ctx, query, args...).Scan(
		&wa.ID, &wa.Network, &wa.Address, &hex40Scan,
		&wa.SubscribedAt, &wa.LastSeenAt, &wa.ExpiresAt, &wa.Active,
		&disableWH, &metaBytes, &balance, &syncedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get watched address: %w", err)
	}
	wa.Hex40 = hex40Scan.String
	wa.DisableWebhooks = disableWH != 0
	wa.Meta = metaBytes
	if balance.Valid {
		wa.Balance = &balance.Float64
	}
	if syncedAt.Valid {
		wa.SyncedAt = &syncedAt.Time
	}

	webhooks, err := GetAddressWebhooks(ctx, db, wa.ID)
	if err != nil {
		return nil, err
	}
	wa.Webhooks = webhooks

	return &wa, nil
}

// GetWatchedByID returns a watched address by its database ID.
func GetWatchedByID(ctx context.Context, db *sql.DB, id int) (*WatchedAddress, error) {
	var wa WatchedAddress
	var metaBytes []byte
	var hex40Scan sql.NullString
	var disableWH int
	err := db.QueryRowContext(ctx,
		`SELECT id, network, address, hex40, subscribed_at, last_seen_at, expires_at, active, disable_webhooks, meta
		 FROM watched_addresses WHERE id = ?`, id,
	).Scan(
		&wa.ID, &wa.Network, &wa.Address, &hex40Scan,
		&wa.SubscribedAt, &wa.LastSeenAt, &wa.ExpiresAt, &wa.Active,
		&disableWH, &metaBytes,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get watched address by id: %w", err)
	}
	wa.Hex40 = hex40Scan.String
	wa.DisableWebhooks = disableWH != 0
	wa.Meta = metaBytes
	return &wa, nil
}

// ListWatched returns a paginated list of watched addresses with summary stats.
// search filters by address substring (pass "" to disable).
func ListWatched(ctx context.Context, db *sql.DB, limit, offset int, active *bool, network, search string) ([]WatchedAddressSummary, int, error) {
	where := " WHERE 1=1"
	args := []interface{}{}

	if active != nil {
		val := 0
		if *active {
			val = 1
		}
		where += " AND wa.active = ?"
		args = append(args, val)
	}
	if network != "" {
		where += " AND wa.network = ?"
		args = append(args, network)
	}
	if search != "" {
		where += " AND wa.address LIKE ?"
		args = append(args, "%"+search+"%")
	}

	var total int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM watched_addresses wa`+where,
		args...,
	).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count watched addresses: %w", err)
	}

	listArgs := append(args, limit, offset)
	rows, err := db.QueryContext(ctx,
		`SELECT wa.id, wa.network, wa.address, wa.hex40, wa.subscribed_at, wa.last_seen_at,
		        wa.expires_at, wa.active, wa.disable_webhooks, wa.meta,
		        COUNT(DISTINCT CASE WHEN aw.active = 1 THEN aw.id END) AS webhooks_count,
		        COUNT(DISTINCT te.id) AS tx_count
		 FROM watched_addresses wa
		 LEFT JOIN address_webhooks aw ON aw.watched_address_id = wa.id
		 LEFT JOIN transfer_events te ON te.watched_address_id = wa.id`+
			where+`
		 GROUP BY wa.id, wa.network, wa.address, wa.hex40, wa.subscribed_at, wa.last_seen_at,
		          wa.expires_at, wa.active, wa.disable_webhooks, wa.meta
		 ORDER BY wa.subscribed_at DESC
		 LIMIT ? OFFSET ?`,
		listArgs...,
	)
	if err != nil {
		return nil, 0, fmt.Errorf("list watched addresses: %w", err)
	}
	defer rows.Close()

	var result []WatchedAddressSummary
	for rows.Next() {
		var s WatchedAddressSummary
		var metaBytes []byte
		var hex40Scan sql.NullString
		var disableWH int
		if err := rows.Scan(
			&s.ID, &s.Network, &s.Address, &hex40Scan,
			&s.SubscribedAt, &s.LastSeenAt, &s.ExpiresAt, &s.Active,
			&disableWH, &metaBytes,
			&s.WebhooksCount, &s.TxCount,
		); err != nil {
			return nil, 0, fmt.Errorf("scan watched address summary: %w", err)
		}
		s.Hex40 = hex40Scan.String
		s.DisableWebhooks = disableWH != 0
		s.Meta = metaBytes
		result = append(result, s)
	}
	return result, total, rows.Err()
}

// GetAddressWebhooks returns all webhooks for a given watched address ID.
func GetAddressWebhooks(ctx context.Context, db *sql.DB, watchedID int) ([]AddressWebhook, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, watched_address_id, url, secret, timeout_sec, max_retries, active, created_at
		 FROM address_webhooks WHERE watched_address_id = ? ORDER BY id DESC`, watchedID,
	)
	if err != nil {
		return nil, fmt.Errorf("get address webhooks: %w", err)
	}
	defer rows.Close()

	var result []AddressWebhook
	for rows.Next() {
		var wh AddressWebhook
		if err := rows.Scan(
			&wh.ID, &wh.WatchedAddressID, &wh.URL, &wh.Secret,
			&wh.TimeoutSec, &wh.MaxRetries, &wh.Active, &wh.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan webhook: %w", err)
		}
		result = append(result, wh)
	}
	return result, rows.Err()
}

// AddAddressWebhook inserts a new webhook for a watched address and returns the created record.
func AddAddressWebhook(ctx context.Context, db *sql.DB, watchedID int, req WebhookRequest) (*AddressWebhook, error) {
	timeoutSec := req.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = 10
	}
	maxRetries := req.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	result, err := db.ExecContext(ctx,
		`INSERT INTO address_webhooks (watched_address_id, url, secret, timeout_sec, max_retries, active)
		 VALUES (?, ?, ?, ?, ?, 1)`,
		watchedID, req.URL, req.Secret, timeoutSec, maxRetries,
	)
	if err != nil {
		return nil, fmt.Errorf("insert webhook: %w", err)
	}
	id, _ := result.LastInsertId()

	var wh AddressWebhook
	err = db.QueryRowContext(ctx,
		`SELECT id, watched_address_id, url, secret, timeout_sec, max_retries, active, created_at
		 FROM address_webhooks WHERE id = ?`, id,
	).Scan(&wh.ID, &wh.WatchedAddressID, &wh.URL, &wh.Secret, &wh.TimeoutSec, &wh.MaxRetries, &wh.Active, &wh.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("fetch webhook: %w", err)
	}
	return &wh, nil
}

// DeleteAddressWebhook removes a webhook by ID, scoped to the given watched address.
// Returns sql.ErrNoRows if the webhook does not exist or belongs to a different address.
func DeleteAddressWebhook(ctx context.Context, db *sql.DB, webhookID, watchedID int) error {
	result, err := db.ExecContext(ctx,
		`DELETE FROM address_webhooks WHERE id = ? AND watched_address_id = ?`,
		webhookID, watchedID,
	)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// IncrBlockchairRequestCount increments the Blockchair API call counter for an address.
func IncrBlockchairRequestCount(ctx context.Context, db *sql.DB, network, address string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE watched_addresses
		 SET blockchair_request_count = blockchair_request_count + 1
		 WHERE network = ? AND address = ?`,
		network, address,
	)
	return err
}

// AdjustWatchedBalance atomically adds delta to the token balance (USDT/USDC/etc.) of a watched address.
// delta is positive for IN transactions and negative for OUT transactions.
// COALESCE initialises the balance from NULL if it has not been set yet.
func AdjustWatchedBalance(ctx context.Context, db *sql.DB, id int, delta float64) error {
	_, err := db.ExecContext(ctx,
		`UPDATE watched_addresses SET balance = COALESCE(balance, 0) + ? WHERE id = ?`,
		delta, id,
	)
	if err != nil {
		return fmt.Errorf("adjust watched balance: %w", err)
	}
	return nil
}

// UpdateAddressBalance stores the current native-currency balance after an initial sync.
func UpdateAddressBalance(ctx context.Context, db *sql.DB, network, address string, balance float64) error {
	_, err := db.ExecContext(ctx,
		`UPDATE watched_addresses SET balance = ?, synced_at = NOW() WHERE network = ? AND address = ?`,
		balance, network, address,
	)
	if err != nil {
		return fmt.Errorf("update address balance: %w", err)
	}
	return nil
}

// TouchLastSeen async-updates last_seen_at for an address.
func TouchLastSeen(ctx context.Context, db *sql.DB, network, address string, ts time.Time) error {
	_, err := db.ExecContext(ctx,
		`UPDATE watched_addresses SET last_seen_at = ? WHERE network = ? AND address = ?`,
		ts, network, address,
	)
	return err
}
