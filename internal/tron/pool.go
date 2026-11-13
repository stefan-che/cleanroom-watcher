package tron

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ErrAllNodesDown is returned when every node in the pool has failed.
var ErrAllNodesDown = errors.New("all tron nodes are unavailable")

// NodePool manages a list of Tron nodes and provides automatic failover.
// When a node fails, it tries the next one in the list.
// When all nodes are down it returns ErrAllNodesDown and starts a background
// recovery goroutine that pings the first node every 5 minutes.
// Once the first node responds, the pool resets to it and resumes normal operation.
type NodePool struct {
	nodes   []*Client
	mu      sync.Mutex
	current int

	// recovery state
	recovering bool
	recovered  chan struct{} // closed when pool recovers; replaced each time all-down starts
}

// NewNodePool creates a NodePool from the given clients.
// Panics if nodes is empty.
func NewNodePool(nodes []*Client) *NodePool {
	if len(nodes) == 0 {
		panic("tron.NewNodePool: at least one node is required")
	}
	return &NodePool{nodes: nodes}
}

func (p *NodePool) GetLatestBlockNumber(ctx context.Context) (int64, error) {
	var result int64
	err := p.do(ctx, func(c *Client) error {
		v, e := c.GetLatestBlockNumber(ctx)
		result = v
		return e
	})
	return result, err
}

func (p *NodePool) GetBlock(ctx context.Context, num int64) (*Block, error) {
	var result *Block
	err := p.do(ctx, func(c *Client) error {
		v, e := c.GetBlock(ctx, num)
		result = v
		return e
	})
	return result, err
}

func (p *NodePool) GetTransactionInfo(ctx context.Context, txID string) (*TransactionInfo, error) {
	var result *TransactionInfo
	err := p.do(ctx, func(c *Client) error {
		v, e := c.GetTransactionInfo(ctx, txID)
		result = v
		return e
	})
	return result, err
}

func (p *NodePool) GetTransactionInfosByBlock(ctx context.Context, num int64) ([]TransactionInfo, error) {
	var result []TransactionInfo
	err := p.do(ctx, func(c *Client) error {
		v, e := c.GetTransactionInfosByBlock(ctx, num)
		result = v
		return e
	})
	return result, err
}

// do tries each node starting from current. On success it updates current.
// If all nodes fail it starts background recovery and returns ErrAllNodesDown.
func (p *NodePool) do(ctx context.Context, fn func(*Client) error) error {
	p.mu.Lock()
	start := p.current
	n := len(p.nodes)
	p.mu.Unlock()

	for i := 0; i < n; i++ {
		idx := (start + i) % n
		client := p.nodes[idx]

		if err := fn(client); err != nil {
			slog.Warn("tron node failed, trying next",
				"node_index", idx,
				"url", client.baseURL,
				"err", err,
			)
			continue
		}

		// Success: if we switched nodes, log it and update current.
		if idx != start {
			slog.Info("tron node switched",
				"from_index", start,
				"to_index", idx,
				"url", client.baseURL,
			)
			p.mu.Lock()
			p.current = idx
			p.mu.Unlock()
		}
		return nil
	}

	// All nodes failed — start background recovery if not already running.
	p.mu.Lock()
	if !p.recovering {
		p.recovering = true
		ch := make(chan struct{})
		p.recovered = ch
		go p.recover(ctx, ch)
	}
	p.mu.Unlock()

	return ErrAllNodesDown
}

// recover pings the first node every 5 minutes until it responds or ctx is cancelled.
func (p *NodePool) recover(ctx context.Context, done chan struct{}) {
	slog.Warn("all tron nodes are down, starting recovery (ping every 5 minutes)")
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			p.mu.Lock()
			p.recovering = false
			p.mu.Unlock()
			return
		case <-ticker.C:
			_, err := p.nodes[0].GetLatestBlockNumber(ctx)
			if err != nil {
				slog.Warn("tron node[0] still unavailable during recovery", "url", p.nodes[0].baseURL, "err", err)
				continue
			}

			slog.Info("tron node[0] recovered", "url", p.nodes[0].baseURL)
			p.mu.Lock()
			p.current = 0
			p.recovering = false
			p.mu.Unlock()
			close(done)
			return
		}
	}
}
