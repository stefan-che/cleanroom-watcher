package webhook

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/stefan-che/cleanroom-watcher/internal/db"
)

const defaultConcurrency = 10

// DeliveryJob is a task for the webhook dispatcher.
type DeliveryJob struct {
	DeliveryID int64
	EventID    int64
	Payload    EventPayload
	WebhookURL string
	Secret     string
	TimeoutSec int
	MaxRetries int
}

// Dispatcher manages a pool of webhook sender workers.
type Dispatcher struct {
	jobCh  chan DeliveryJob
	db     *sql.DB
	sender *Sender
}

// NewDispatcher creates and starts a webhook dispatcher with the given concurrency.
func NewDispatcher(ctx context.Context, sqlDB *sql.DB, concurrency int) *Dispatcher {
	if concurrency <= 0 {
		concurrency = defaultConcurrency
	}

	d := &Dispatcher{
		jobCh:  make(chan DeliveryJob, 1000),
		db:     sqlDB,
		sender: NewSender(),
	}

	for i := 0; i < concurrency; i++ {
		go d.worker(ctx)
	}

	return d
}

// Dispatch sends a job to the dispatcher queue.
func (d *Dispatcher) Dispatch(job DeliveryJob) {
	select {
	case d.jobCh <- job:
	default:
		slog.Warn("webhook dispatcher queue full, dropping job", "delivery_id", job.DeliveryID)
	}
}

func (d *Dispatcher) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-d.jobCh:
			if !ok {
				return
			}
			d.process(ctx, job)
		}
	}
}

func (d *Dispatcher) process(ctx context.Context, job DeliveryJob) {
	statusCode, err := d.sender.Send(ctx, job.WebhookURL, job.Secret, job.Payload, job.TimeoutSec)

	success := err == nil && statusCode >= http.StatusOK && statusCode < 300

	var errMsg string
	if err != nil {
		errMsg = err.Error()
	} else if !success {
		errMsg = "unexpected status code"
	}

	// Determine next retry time based on current attempt count
	var nextRetry *time.Time
	if !success {
		var attempts int
		_ = d.db.QueryRowContext(ctx,
			`SELECT attempts FROM webhook_deliveries WHERE id = ?`,
			job.DeliveryID,
		).Scan(&attempts)

		nextAttempt := attempts + 1
		maxRetries := job.MaxRetries
		if maxRetries <= 0 {
			maxRetries = 3
		}

		if nextAttempt < maxRetries {
			var delay time.Duration
			switch nextAttempt {
			case 1:
				delay = 5 * time.Second
			case 2:
				delay = 30 * time.Second
			default:
				delay = 60 * time.Second
			}
			t := time.Now().Add(delay)
			nextRetry = &t
		}
	}

	if updateErr := db.UpdateDeliveryAttempt(ctx, d.db, job.DeliveryID, success, statusCode, errMsg, nextRetry); updateErr != nil {
		slog.Error("update delivery attempt", "delivery_id", job.DeliveryID, "err", updateErr)
	}

	if success {
		slog.Info("webhook delivered", "delivery_id", job.DeliveryID, "url", job.WebhookURL, "status", statusCode)
	} else {
		slog.Warn("webhook delivery failed", "delivery_id", job.DeliveryID, "url", job.WebhookURL, "status", statusCode, "err", errMsg)
	}
}
