package scanner

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/config"
	"github.com/stefan-che/cleanroom-watcher/internal/db"
	"github.com/stefan-che/cleanroom-watcher/internal/tron"
)

// Scanner is the main blockchain scanning loop.
type Scanner struct {
	processor *Processor
	db        *sql.DB
	cfg       *config.Config
}

// NewScanner creates a new Scanner.
func NewScanner(processor *Processor, sqlDB *sql.DB, cfg *config.Config) *Scanner {
	return &Scanner{
		processor: processor,
		db:        sqlDB,
		cfg:       cfg,
	}
}

const recoveryInterval = 5 * time.Minute

// Run starts the scanning loop. Blocks until ctx is cancelled.
// When all Tron nodes are unavailable the scanner switches to a slow
// recovery mode: it pings every 5 minutes and resumes normal operation
// as soon as any node responds.
func (s *Scanner) Run(ctx context.Context) error {
	normalInterval := time.Duration(s.cfg.Scanner.ScanIntervalMS) * time.Millisecond
	ticker := time.NewTicker(normalInterval)
	defer ticker.Stop()

	allDown := false

	slog.Info("scanner started",
		"interval_ms", s.cfg.Scanner.ScanIntervalMS,
		"confirmations", s.cfg.Scanner.Confirmations,
		"scan_step", s.cfg.Scanner.ScanStep,
	)

	for {
		select {
		case <-ctx.Done():
			slog.Info("scanner stopped")
			return ctx.Err()
		case <-ticker.C:
			err := s.tickOnce(ctx)
			switch {
			case err == nil:
				if allDown {
					allDown = false
					slog.Info("tron node recovered, resuming normal scanning", "interval_ms", s.cfg.Scanner.ScanIntervalMS)
					ticker.Reset(normalInterval)
				}
			case errors.Is(err, tron.ErrAllNodesDown):
				if !allDown {
					allDown = true
					slog.Warn("all tron nodes are down, switching to recovery mode",
						"retry_interval", recoveryInterval,
					)
					ticker.Reset(recoveryInterval)
				}
			default:
				slog.Error("scanner tick error", "err", err)
			}
		}
	}
}

// tickOnce performs a single scan tick.
func (s *Scanner) tickOnce(ctx context.Context) error {
	// 1. Get latest block number
	latestBlock, err := s.processor.tronClient.GetLatestBlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}

	// 2. Calculate safe block (confirmed)
	safeBlock := latestBlock - s.cfg.Scanner.Confirmations

	// 3. Get last processed block
	lastBlock, err := db.GetLastBlock(ctx, s.db)
	if err != nil {
		return fmt.Errorf("get last block: %w", err)
	}

	// Take the higher of DB value and config start_block.
	// This lets start_block act as a "fast-forward" when set above current DB position.
	if cfgStart := s.cfg.Scanner.StartBlock - 1; cfgStart > lastBlock {
		lastBlock = cfgStart
	}

	// 4. Nothing to process
	if safeBlock <= lastBlock {
		slog.Debug("scanner: up to date", "safe_block", safeBlock, "last_block", lastBlock)
		return nil
	}

	// 5. Calculate end block for this tick
	end := lastBlock + s.cfg.Scanner.ScanStep
	if end > safeBlock {
		end = safeBlock
	}

	tickStart := time.Now()
	count := int(end - lastBlock)
	workers := s.cfg.Scanner.ScanWorkers
	if workers > count {
		workers = count
	}

	slog.Info("scanner tick",
		"from", lastBlock+1,
		"to", end,
		"blocks", count,
		"workers", workers,
	)

	// 6. Distribute blocks among workers: each goroutine gets its own
	// contiguous sub-range and processes it sequentially.
	// blockErrs[i] holds the error for block (lastBlock+1+i), nil = success.
	blockErrs := make([]error, count)
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		startIdx := w * count / workers     // inclusive, 0-based
		endIdx := (w + 1) * count / workers // exclusive

		wg.Add(1)
		go func(startIdx, endIdx int) {
			defer wg.Done()
			for i := startIdx; i < endIdx; i++ {
				select {
				case <-ctx.Done():
					blockErrs[i] = ctx.Err()
					return
				default:
				}

				h := lastBlock + int64(i) + 1
				if err := s.processor.processBlock(ctx, h); err != nil {
					blockErrs[i] = err
					return
				}
			}
		}(startIdx, endIdx)
	}
	wg.Wait()

	// 7. Advance last_block up to the longest contiguous successful prefix.
	// If worker-2 fails at block N, blocks processed by workers 3..20 after N
	// are discarded — they will be re-processed next tick.
	newLastBlock := lastBlock
	for i := 0; i < count; i++ {
		if blockErrs[i] != nil {
			slog.Error("process block error", "block", lastBlock+int64(i)+1, "err", blockErrs[i])
			break
		}
		newLastBlock = lastBlock + int64(i) + 1
	}

	if newLastBlock > lastBlock {
		if err := db.SetLastBlock(ctx, s.db, newLastBlock); err != nil {
			return fmt.Errorf("set last block %d: %w", newLastBlock, err)
		}
	}

	elapsed := time.Since(tickStart)
	processed := int(newLastBlock - lastBlock)
	bps := float64(processed) / elapsed.Seconds()
	slog.Info("tick done",
		"processed", processed,
		"elapsed_ms", elapsed.Milliseconds(),
		"blocks_per_sec", fmt.Sprintf("%.1f", bps),
	)

	if newLastBlock < end {
		return fmt.Errorf("process block %d: %w", newLastBlock+1, blockErrs[int(newLastBlock-lastBlock)])
	}
	return nil
}
