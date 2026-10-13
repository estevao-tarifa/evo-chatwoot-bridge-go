// Package worker is a domain-agnostic bounded job queue with a fixed-size
// worker pool (ADR-0005). It moves opaque func(context.Context) error jobs and
// imports only the standard library: no net/http, no database/sql and no
// concrete bridge/evolution/chatwoot client (ARCHITECTURE §2 rule 4). The
// per-job timeout is derived from the process context, never from a request.
package worker

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultCapacity   = 1024
	defaultMaxBytes   = 64 << 20 // 64 MiB
	defaultWorkers    = 8
	defaultJobTimeout = 30 * time.Second
)

// Config bounds the queue. A zero field selects its default.
type Config struct {
	Capacity   int           // max admitted-but-unfinished jobs
	MaxBytes   int64         // max admitted-but-unfinished payload bytes
	Workers    int           // fixed pool size
	JobTimeout time.Duration // per-job deadline derived from the process ctx
}

// job is one queued unit: the work and the byte size reserved for it.
type job struct {
	fn   func(context.Context) error
	size int
}

// Queue is a bounded, non-blocking job queue with a fixed worker pool. It is
// safe for concurrent use. One lock guards both the admission counters and the
// closed flag so Stop can never race a TryEnqueue send on a closed channel; an
// RWMutex adds nothing because every admission mutates a counter.
type Queue struct {
	cfg    Config
	logger *slog.Logger

	mu       sync.Mutex
	jobs     chan job
	closed   bool
	pending  int
	pendingB int64

	rejected atomic.Int64
	wg       sync.WaitGroup
}

// New builds a Queue. A zero field in cfg selects its default; a negative
// field is a programming error and panics.
func New(cfg Config, logger *slog.Logger) *Queue {
	if cfg.Capacity < 0 || cfg.MaxBytes < 0 || cfg.Workers < 0 || cfg.JobTimeout < 0 {
		panic("worker: New requires non-negative configuration")
	}
	if cfg.Capacity == 0 {
		cfg.Capacity = defaultCapacity
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = defaultMaxBytes
	}
	if cfg.Workers == 0 {
		cfg.Workers = defaultWorkers
	}
	if cfg.JobTimeout == 0 {
		cfg.JobTimeout = defaultJobTimeout
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Queue{
		cfg:    cfg,
		logger: logger,
		jobs:   make(chan job, cfg.Capacity),
	}
}

// Start spawns the fixed worker pool. Jobs run under a per-job timeout derived
// from ctx; the caller owns ctx and cancels it to abort in-flight work.
func (q *Queue) Start(ctx context.Context) {
	q.wg.Add(q.cfg.Workers)
	for range q.cfg.Workers {
		go q.worker(ctx)
	}
}

// TryEnqueue admits a job without blocking. It reserves the count and byte
// budget atomically and returns false when either is exhausted or intake has
// stopped; false means the caller must not treat the job as accepted. A nil fn
// is rejected. The bytes/count are released when the job finishes.
func (q *Queue) TryEnqueue(fn func(context.Context) error, size int) bool {
	if fn == nil {
		q.rejected.Add(1)
		return false
	}
	if size < 0 {
		size = 0
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.pending >= q.cfg.Capacity || q.pendingB+int64(size) > q.cfg.MaxBytes {
		q.rejected.Add(1)
		return false
	}
	select {
	case q.jobs <- job{fn: fn, size: size}:
		q.pending++
		q.pendingB += int64(size)
		return true
	default:
		q.rejected.Add(1)
		return false
	}
}

// Stop closes intake. It is idempotent and safe against a concurrent
// TryEnqueue: the closed flag and the channel close happen under the same lock
// the sender holds. Workers drain the buffered jobs and exit.
func (q *Queue) Stop() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.closed = true
	close(q.jobs)
}

// Wait returns when the pool has exited, or ctx.Err() when ctx expires first.
// On expiry the caller cancels the process context; aborted jobs leave their
// rows 'processing' for the next-boot reconcile.
func (q *Queue) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		q.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Depth returns the number of buffered jobs (not yet picked up by a worker).
func (q *Queue) Depth() int {
	return len(q.jobs)
}

// Rejected returns how many TryEnqueue calls were refused (capacity, byte
// budget or stopped intake).
func (q *Queue) Rejected() int64 {
	return q.rejected.Load()
}

func (q *Queue) worker(ctx context.Context) {
	defer q.wg.Done()
	for j := range q.jobs {
		q.run(ctx, j)
	}
}

func (q *Queue) run(ctx context.Context, j job) {
	defer q.release(j.size)
	// A panicking job must not crash the process: recover, log with the stack,
	// and return. The row stays 'processing' for the next-boot reconcile
	// (ADR-0004 Amendment 3). Defer order is LIFO, so this runs before release.
	defer func() {
		if rec := recover(); rec != nil {
			q.logger.Error("worker: job panicked", "panic", rec, "stack", string(debug.Stack()))
		}
	}()
	jobCtx, cancel := context.WithTimeout(ctx, q.cfg.JobTimeout)
	defer cancel()
	if err := j.fn(jobCtx); err != nil {
		q.logger.Warn("worker: job failed", "error", err)
	}
}

func (q *Queue) release(size int) {
	q.mu.Lock()
	q.pending--
	q.pendingB -= int64(size)
	q.mu.Unlock()
}
