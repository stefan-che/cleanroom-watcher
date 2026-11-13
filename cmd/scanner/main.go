package main

import (
	"context"
	stdlibsql "database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/admin"
	"github.com/stefan-che/cleanroom-watcher/internal/api"
	"github.com/stefan-che/cleanroom-watcher/internal/blockchair"
	"github.com/stefan-che/cleanroom-watcher/internal/cache"
	"github.com/stefan-che/cleanroom-watcher/internal/config"
	"github.com/stefan-che/cleanroom-watcher/internal/db"
	"github.com/stefan-che/cleanroom-watcher/internal/scanner"
	"github.com/stefan-che/cleanroom-watcher/internal/source"
	"github.com/stefan-che/cleanroom-watcher/internal/tron"
	"github.com/stefan-che/cleanroom-watcher/internal/tronscan"
	"github.com/stefan-che/cleanroom-watcher/internal/webhook"

	"github.com/golang-migrate/migrate/v4"
	migmysql "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/redis/go-redis/v9"
)

func main() {
	logWriter, err := newDailyWriter("logs")
	if err != nil {
		// if the file cannot be opened, log to stdout only
		fmt.Fprintf(os.Stderr, "warn: cannot open log dir: %v\n", err)
		logWriter = nil
	}

	var out io.Writer = os.Stdout
	if logWriter != nil {
		out = io.MultiWriter(os.Stdout, logWriter)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if err := run(); err != nil {
		slog.Error("fatal error", "err", err)
		os.Exit(1)
	}
}

// dailyWriter writes to logs/YYYY-MM-DD.log, rotating at midnight.
type dailyWriter struct {
	mu   sync.Mutex
	dir  string
	file *os.File
	day  string
}

func newDailyWriter(dir string) (*dailyWriter, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	w := &dailyWriter{dir: dir}
	if err := w.rotate(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *dailyWriter) rotate() error {
	day := time.Now().Format("2006-01-02")
	if w.file != nil && w.day == day {
		return nil
	}
	if w.file != nil {
		_ = w.file.Close()
	}
	f, err := os.OpenFile(filepath.Join(w.dir, day+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	w.file = f
	w.day = day
	return nil
}

func (w *dailyWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.rotate()
	return w.file.Write(p)
}

func run() error {
	configFile := os.Getenv("CONFIG_FILE")
	if configFile == "" {
		configFile = "config.json"
	}
	cfg, err := config.Load(configFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Register built-in blockchain sources.
	// To add a new source: implement source.BlockchainPoller and call source.Register here.
	source.Register("blockchair", func(deps source.Deps) source.BlockchainPoller {
		return blockchair.NewPoller(deps.DB, deps.Config, deps.Dispatcher)
	})

	externalNetworks := map[string]int{}
	for _, bc := range cfg.Blockchains {
		if bc.Source != "tron_node" && bc.Enabled {
			externalNetworks[bc.Source]++
		}
	}
	slog.Info("configuration loaded",
		"tron_nodes", len(cfg.TronNodes),
		"api_listen", cfg.API.Listen,
		"scan_step", cfg.Scanner.ScanStep,
		"external_sources", externalNetworks,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// MySQL
	slog.Info("connecting to MySQL...")
	sqlDB, err := db.NewDB(ctx, cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("connect mysql: %w", err)
	}
	defer sqlDB.Close()
	slog.Info("MySQL connected")

	// Migrations
	slog.Info("running migrations...")
	if err := runMigrations(cfg.DBDSN); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	slog.Info("migrations complete")

	// Redis (optional)
	var rdb *redis.Client
	slog.Info("connecting to Redis...", "addr", cfg.RedisAddr)
	rdb = redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		slog.Warn("Redis not available, continuing without Redis", "err", err)
		rdb = nil
	} else {
		slog.Info("Redis connected")
		defer rdb.Close()
	}

	// WatchCache (TRON in-memory cache)
	slog.Info("initializing watch cache...")
	watchCache, err := cache.New(ctx, sqlDB, rdb, cfg)
	if err != nil {
		return fmt.Errorf("init watch cache: %w", err)
	}
	slog.Info("watch cache ready", "tron_active", watchCache.ActiveCount())

	// TRON node pool
	tronNodes := make([]*tron.Client, 0, len(cfg.TronNodes))
	for _, n := range cfg.TronNodes {
		tronNodes = append(tronNodes, tron.NewClient(n.URL, n.APIKey))
	}
	tronClient := tron.NewNodePool(tronNodes)
	slog.Info("tron node pool created", "nodes", len(tronNodes))

	// Webhook dispatcher + retry worker
	dispatcher := webhook.NewDispatcher(ctx, sqlDB, cfg.WebhookConcurrency)
	slog.Info("webhook dispatcher started", "concurrency", cfg.WebhookConcurrency)

	retryWorker := webhook.NewRetryWorker(sqlDB, dispatcher)
	go retryWorker.Run(ctx)
	slog.Info("retry worker started")

	// HTTP API
	mux := http.NewServeMux()
	bcClient := blockchair.NewClient(cfg.Blockchair.APIKey)
	tsClient := tronscan.NewClient()
	handler := api.NewHandler(sqlDB, watchCache, tronClient, rdb, cfg, bcClient, tsClient)
	handler.RegisterRoutes(mux)

	// Admin panel
	adminHandler := admin.NewHandler(cfg, sqlDB, "logs")
	adminHandler.RegisterRoutes(mux)
	if cfg.Admin.Enabled() {
		slog.Info("admin panel enabled", "path", "/"+cfg.Admin.Path)
	}

	httpServer := &http.Server{
		Addr:         cfg.API.Listen,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("HTTP API server starting", "addr", cfg.API.Listen)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("HTTP server error", "err", err)
			cancel()
		}
	}()

	// TRON scanner
	processor := scanner.NewProcessor(tronClient, watchCache, sqlDB, dispatcher, cfg)
	sc := scanner.NewScanner(processor, sqlDB, cfg)

	scannerDone := make(chan error, 1)
	go func() {
		scannerDone <- sc.Run(ctx)
	}()
	slog.Info("tron scanner started")

	// Start one poller goroutine per distinct external source (e.g., "blockchair").
	// Each source handles all chains assigned to it in config.blockchains[].source.
	deps := source.Deps{DB: sqlDB, Config: cfg, Dispatcher: dispatcher}
	for sourceName, count := range externalNetworks {
		p := source.New(sourceName, deps)
		if p == nil {
			slog.Warn("no poller registered for source, skipping", "source", sourceName)
			continue
		}
		go p.Run(ctx)
		slog.Info("source poller started", "source", sourceName, "chains", count)
	}

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	select {
	case sig := <-sigCh:
		slog.Info("received signal, shutting down", "signal", sig)
	case err := <-scannerDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("scanner exited with error", "err", err)
		}
	}

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("HTTP server shutdown error", "err", err)
	}

	slog.Info("shutdown complete")
	return nil
}

func runMigrations(dsn string) error {
	migDSN := dsn
	if !strings.Contains(migDSN, "multiStatements=true") {
		if strings.Contains(migDSN, "?") {
			migDSN += "&multiStatements=true"
		} else {
			migDSN += "?multiStatements=true"
		}
	}

	migDB, err := stdlibsql.Open("mysql", migDSN)
	if err != nil {
		return fmt.Errorf("open migration db: %w", err)
	}
	defer migDB.Close()

	driver, err := migmysql.WithInstance(migDB, &migmysql.Config{})
	if err != nil {
		return fmt.Errorf("create mysql driver: %w", err)
	}

	m, err := migrate.NewWithDatabaseInstance("file://migrations", "mysql", driver)
	if err != nil {
		return fmt.Errorf("create migrate: %w", err)
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}

	return nil
}
