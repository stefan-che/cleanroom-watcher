package cache

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/config"
	"github.com/stefan-che/cleanroom-watcher/internal/db"
	"github.com/stefan-che/cleanroom-watcher/internal/tron"

	"github.com/redis/go-redis/v9"
)

// CacheEntry holds the data stored for each TRON watched address.
type CacheEntry struct {
	AddressID int
	Address   string // base58 for TRON
}

// WatchRequest is the input for adding a new watched address to cache.
type WatchRequest struct {
	Network         string
	Address         string // canonical address for the network
	Hex40           string // TRON only
	TTLHours        int
	DisableWebhooks bool
	Webhooks        []db.WebhookRequest
	Meta            []byte
}

// WatchCache provides fast in-memory lookups for TRON addresses,
// backed by Redis and MySQL. Non-TRON networks are persisted to MySQL only.
type WatchCache struct {
	mu   sync.RWMutex
	data map[string]CacheEntry // hex40 → entry (TRON only)

	rdb    *redis.Client
	db     *sql.DB
	config *config.Config

	touchCh chan touchMsg

	redisAvailable bool
}

type touchMsg struct {
	hex40 string
	ts    time.Time
}

// New creates and initializes a new WatchCache.
func New(ctx context.Context, sqlDB *sql.DB, rdb *redis.Client, cfg *config.Config) (*WatchCache, error) {
	c := &WatchCache{
		data:    make(map[string]CacheEntry),
		rdb:     rdb,
		db:      sqlDB,
		config:  cfg,
		touchCh: make(chan touchMsg, 4096),
	}

	if rdb != nil {
		if err := rdb.Ping(ctx).Err(); err != nil {
			slog.Warn("Redis unavailable, falling back to MySQL-only mode", "err", err)
			c.redisAvailable = false
		} else {
			c.redisAvailable = true
		}
	}

	// Load active TRON addresses from MySQL
	addresses, err := db.GetAllActiveByNetwork(ctx, sqlDB, "tron")
	if err != nil {
		return nil, fmt.Errorf("load active tron addresses: %w", err)
	}

	if c.redisAvailable && rdb != nil {
		if err := rdb.ConfigSet(ctx, "notify-keyspace-events", "Ex").Err(); err != nil {
			slog.Warn("could not set Redis keyspace events", "err", err)
		}

		pipe := rdb.Pipeline()
		for _, addr := range addresses {
			if addr.Hex40 == "" {
				continue
			}
			key := redisKey(addr.Hex40)
			ttl := time.Until(addr.ExpiresAt)
			if ttl <= 0 {
				ttl = time.Duration(cfg.Scanner.WatchDefaultTTLHours) * time.Hour
			}
			pipe.Set(ctx, key, addr.ID, ttl)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			slog.Warn("Redis pipeline exec error", "err", err)
		}
	}

	c.mu.Lock()
	for _, addr := range addresses {
		if addr.Hex40 == "" {
			continue
		}
		c.data[addr.Hex40] = CacheEntry{
			AddressID: addr.ID,
			Address:   addr.Address,
		}
	}
	c.mu.Unlock()

	slog.Info("WatchCache initialized", "tron_count", len(addresses))

	go c.touchWorker(ctx)

	if c.redisAvailable && rdb != nil {
		go c.listenExpiry(ctx)
	}
	// Always run pollExpiry: handles expiry extension/cleanup for all non-TRON networks,
	// and acts as a fallback for TRON when Redis is unavailable.
	go c.pollExpiry(ctx)

	return c, nil
}

// Contains returns the cache entry for a TRON hex40 address if it exists.
func (c *WatchCache) Contains(hex40 string) (CacheEntry, bool) {
	c.mu.RLock()
	entry, ok := c.data[hex40]
	c.mu.RUnlock()
	return entry, ok
}

// ContainsAny returns a map of hex40 → CacheEntry for all matching TRON addresses.
func (c *WatchCache) ContainsAny(hex40s []string) map[string]CacheEntry {
	result := make(map[string]CacheEntry)
	c.mu.RLock()
	for _, h := range hex40s {
		if entry, ok := c.data[h]; ok {
			result[h] = entry
		}
	}
	c.mu.RUnlock()
	return result
}

// Add adds a new watched address to MySQL, and to Redis + in-memory map if TRON.
func (c *WatchCache) Add(ctx context.Context, req WatchRequest) error {
	dbReq := db.WatchRequest{
		Network:         req.Network,
		Address:         req.Address,
		Hex40:           req.Hex40,
		TTLHours:        req.TTLHours,
		DisableWebhooks: req.DisableWebhooks,
		Webhooks:        req.Webhooks,
		Meta:            req.Meta,
	}

	wa, err := db.AddWatched(ctx, c.db, dbReq)
	if err != nil {
		return fmt.Errorf("add watched: %w", err)
	}

	// In-memory cache + Redis only for TRON (used by the block scanner)
	if req.Network == "tron" && req.Hex40 != "" {
		if c.redisAvailable && c.rdb != nil {
			ttlHours := req.TTLHours
			if ttlHours <= 0 {
				ttlHours = c.config.Scanner.WatchDefaultTTLHours
			}
			ttl := time.Duration(ttlHours) * time.Hour
			if err := c.rdb.Set(ctx, redisKey(req.Hex40), wa.ID, ttl).Err(); err != nil {
				slog.Warn("Redis set error", "hex40", req.Hex40, "err", err)
			}
		}

		c.mu.Lock()
		c.data[req.Hex40] = CacheEntry{
			AddressID: wa.ID,
			Address:   req.Address,
		}
		c.mu.Unlock()
	}

	return nil
}

// Remove removes a watched address from MySQL, and from Redis + in-memory map if TRON.
func (c *WatchCache) Remove(ctx context.Context, network, address string) error {
	if err := db.RemoveWatched(ctx, c.db, network, address); err != nil {
		return fmt.Errorf("remove watched: %w", err)
	}

	if network == "tron" {
		hex40, err := tron.Base58ToHex40(address)
		if err == nil && hex40 != "" {
			if c.redisAvailable && c.rdb != nil {
				if err := c.rdb.Del(ctx, redisKey(hex40)).Err(); err != nil {
					slog.Warn("Redis del error", "hex40", hex40, "err", err)
				}
			}
			c.mu.Lock()
			delete(c.data, hex40)
			c.mu.Unlock()
		}
	}

	return nil
}

// TouchActivity asynchronously updates last_seen_at for a TRON hex40 address.
func (c *WatchCache) TouchActivity(hex40 string, ts time.Time) {
	select {
	case c.touchCh <- touchMsg{hex40: hex40, ts: ts}:
	default:
	}
}

// ActiveCount returns the number of TRON addresses in the in-memory cache.
func (c *WatchCache) ActiveCount() int {
	c.mu.RLock()
	n := len(c.data)
	c.mu.RUnlock()
	return n
}

// Hex40ToBase58 converts a hex40 to base58 via in-memory lookup or direct conversion.
func (c *WatchCache) Hex40ToBase58(hex40 string) string {
	c.mu.RLock()
	entry, ok := c.data[hex40]
	c.mu.RUnlock()
	if ok {
		return entry.Address
	}
	b58, err := tron.Hex40ToBase58(hex40)
	if err != nil {
		return ""
	}
	return b58
}

func (c *WatchCache) touchWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-c.touchCh:
			if !ok {
				return
			}
			_, err := c.db.ExecContext(ctx,
				`UPDATE watched_addresses SET last_seen_at = ? WHERE network = 'tron' AND hex40 = ?`,
				msg.ts, msg.hex40,
			)
			if err != nil {
				slog.Error("touch activity error", "hex40", msg.hex40, "err", err)
			}
		}
	}
}

func (c *WatchCache) listenExpiry(ctx context.Context) {
	dbIdx := c.config.RedisDB
	channel := fmt.Sprintf("__keyevent@%d__:expired", dbIdx)

	pubsub := c.rdb.PSubscribe(ctx, channel)
	defer pubsub.Close()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			key := msg.Payload
			if len(key) <= 8 || key[:8] != "watched:" {
				continue
			}
			hex40 := key[8:]

			var addrID int
			var balance sql.NullFloat64
			qErr := c.db.QueryRowContext(ctx,
				`SELECT id, balance FROM watched_addresses WHERE network = 'tron' AND hex40 = ?`, hex40,
			).Scan(&addrID, &balance)

			if qErr == nil && balance.Valid && balance.Float64 > 0 {
				if _, err := c.db.ExecContext(ctx,
					`UPDATE watched_addresses SET expires_at = DATE_ADD(NOW(), INTERVAL 30 DAY) WHERE network = 'tron' AND hex40 = ?`, hex40,
				); err != nil {
					slog.Error("extend expiry for balance address", "hex40", hex40, "err", err)
				} else {
					if err := c.rdb.Set(ctx, redisKey(hex40), addrID, 30*24*time.Hour).Err(); err != nil {
						slog.Warn("re-set Redis key after expiry extension", "hex40", hex40, "err", err)
					}
					slog.Info("tron address expiry extended (balance > 0)", "hex40", hex40, "balance", balance.Float64)
				}
				continue
			}

			c.mu.Lock()
			delete(c.data, hex40)
			c.mu.Unlock()

			if _, err := c.db.ExecContext(ctx,
				`UPDATE watched_addresses SET active = 0 WHERE network = 'tron' AND hex40 = ?`, hex40,
			); err != nil {
				slog.Error("expire address in mysql", "hex40", hex40, "err", err)
			} else {
				slog.Info("tron address expired", "hex40", hex40)
			}
		}
	}
}

func (c *WatchCache) pollExpiry(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.cleanExpired(ctx)
		}
	}
}

func (c *WatchCache) cleanExpired(ctx context.Context) {
	// Extend expiry by 30 days for addresses that still have a positive balance.
	extResult, err := c.db.ExecContext(ctx,
		`UPDATE watched_addresses
		 SET expires_at = DATE_ADD(NOW(), INTERVAL 30 DAY)
		 WHERE active = 1 AND expires_at <= NOW() AND balance > 0`,
	)
	if err != nil {
		slog.Error("clean expired addresses: extend balance", "err", err)
	} else if n, _ := extResult.RowsAffected(); n > 0 {
		slog.Info("extended expiry for addresses with balance", "count", n)
	}

	// Collect TRON hex40s that will actually be deactivated (no balance).
	rows, err := c.db.QueryContext(ctx,
		`SELECT hex40 FROM watched_addresses
		 WHERE network = 'tron' AND active = 1 AND expires_at <= NOW()
		   AND (balance IS NULL OR balance <= 0) AND hex40 IS NOT NULL`,
	)
	if err != nil {
		slog.Error("clean expired addresses: query", "err", err)
		return
	}

	var hex40s []string
	for rows.Next() {
		var hex40 string
		if err := rows.Scan(&hex40); err == nil {
			hex40s = append(hex40s, hex40)
		}
	}
	rows.Close()

	if len(hex40s) == 0 {
		return
	}

	if _, err := c.db.ExecContext(ctx,
		`UPDATE watched_addresses SET active = 0
		 WHERE active = 1 AND expires_at <= NOW() AND (balance IS NULL OR balance <= 0)`,
	); err != nil {
		slog.Error("clean expired addresses: update", "err", err)
		return
	}

	c.mu.Lock()
	for _, h := range hex40s {
		delete(c.data, h)
	}
	c.mu.Unlock()

	slog.Info("cleaned expired addresses", "count", len(hex40s))
}

func redisKey(hex40 string) string {
	return "watched:" + hex40
}

func parseRedisID(val string) (int, error) {
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0, fmt.Errorf("parse redis id: %w", err)
	}
	return n, nil
}

var _ = parseRedisID
