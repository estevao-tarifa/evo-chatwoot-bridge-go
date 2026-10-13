package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testQueue(cfg Config) *Queue {
	return New(cfg, testLogger())
}

// TestSlice4_W1_CapacityBound proves the count bound: over-capacity enqueues are
// refused and counted.
func TestSlice4_W1_CapacityBound(t *testing.T) {
	q := testQueue(Config{Capacity: 2, MaxBytes: 1 << 20, Workers: 1})
	release := make(chan struct{})
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	blocking := func(context.Context) error { <-release; return nil }
	for i := 0; i < 2; i++ {
		if !q.TryEnqueue(blocking, 1) {
			t.Fatalf("TryEnqueue #%d = false, want true", i)
		}
	}
	// Wait until the single worker has picked up one job so the second is
	// buffered; either way pending == capacity now.
	if q.TryEnqueue(blocking, 1) {
		t.Fatal("TryEnqueue over capacity = true, want false")
	}
	if got := q.Rejected(); got != 1 {
		t.Errorf("Rejected = %d, want 1", got)
	}
}

// TestSlice4_W2_ByteBound proves the byte budget: an enqueue that would exceed
// MaxBytes is refused.
func TestSlice4_W2_ByteBound(t *testing.T) {
	q := testQueue(Config{Capacity: 100, MaxBytes: 10, Workers: 1})
	release := make(chan struct{})
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	blocking := func(context.Context) error { <-release; return nil }
	if !q.TryEnqueue(blocking, 10) {
		t.Fatal("TryEnqueue exactly at the byte bound = false, want true")
	}
	if q.TryEnqueue(blocking, 1) {
		t.Fatal("TryEnqueue over the byte bound = true, want false")
	}
	if got := q.Rejected(); got != 1 {
		t.Errorf("Rejected = %d, want 1", got)
	}
}

// TestSlice4_W3_FixedPool proves the observed concurrency never exceeds the
// configured worker count.
func TestSlice4_W3_FixedPool(t *testing.T) {
	const workers = 4
	q := testQueue(Config{Capacity: 100, MaxBytes: 1 << 20, Workers: workers})

	var current, max atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	const jobs = 32
	for i := 0; i < jobs; i++ {
		if !q.TryEnqueue(func(context.Context) error {
			running := current.Add(1)
			for {
				seen := max.Load()
				if running <= seen || max.CompareAndSwap(seen, running) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			current.Add(-1)
			return nil
		}, 1) {
			t.Fatalf("TryEnqueue #%d = false", i)
		}
	}

	q.Stop()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := q.Wait(waitCtx); err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
	if got := max.Load(); got > workers {
		t.Errorf("observed concurrency = %d, want <= %d", got, workers)
	}
}

// TestSlice4_W4_GracefulDrain proves every admitted job completes after Stop.
func TestSlice4_W4_GracefulDrain(t *testing.T) {
	q := testQueue(Config{Capacity: 100, MaxBytes: 1 << 20, Workers: 4})

	var completed atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	const jobs = 20
	for i := 0; i < jobs; i++ {
		if !q.TryEnqueue(func(context.Context) error {
			time.Sleep(5 * time.Millisecond)
			completed.Add(1)
			return nil
		}, 1) {
			t.Fatalf("TryEnqueue #%d = false", i)
		}
	}

	q.Stop()
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer waitCancel()
	if err := q.Wait(waitCtx); err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
	if got := completed.Load(); got != jobs {
		t.Errorf("completed = %d, want %d", got, jobs)
	}
}

// TestSlice4_W5_DrainTimeout proves Wait reports a timeout, the in-flight job's
// context is canceled and nothing panics.
func TestSlice4_W5_DrainTimeout(t *testing.T) {
	q := testQueue(Config{Capacity: 4, MaxBytes: 1 << 20, Workers: 1, JobTimeout: time.Minute})

	ctx, cancel := context.WithCancel(context.Background())
	q.Start(ctx)

	started := make(chan struct{})
	if !q.TryEnqueue(func(jobCtx context.Context) error {
		close(started)
		<-jobCtx.Done() // the process cancel must reach the job
		return jobCtx.Err()
	}, 1) {
		t.Fatal("TryEnqueue = false, want true")
	}
	<-started

	q.Stop()
	if err := waitFor(t, q, 50*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait = %v, want DeadlineExceeded", err)
	}

	cancel() // the caller cancels the process context on drain expiry
	if err := waitFor(t, q, 2*time.Second); err != nil {
		t.Fatalf("Wait after cancel = %v, want nil", err)
	}
}

// TestSlice4_W6_NoGoroutineLeak proves Stop+Wait leaves no goroutine behind.
func TestSlice4_W6_NoGoroutineLeak(t *testing.T) {
	baseline := runtime.NumGoroutine()

	q := testQueue(Config{Capacity: 100, MaxBytes: 1 << 20, Workers: 8})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	for i := 0; i < 32; i++ {
		if !q.TryEnqueue(func(context.Context) error { return nil }, 1) {
			t.Fatalf("TryEnqueue #%d = false", i)
		}
	}
	q.Stop()
	if err := waitFor(t, q, 2*time.Second); err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= baseline+1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("goroutine leak: baseline %d, now %d", baseline, runtime.NumGoroutine())
}

// TestSlice4_W7_ConcurrentEnqueueAndStop proves a concurrent TryEnqueue cannot
// send on a closed channel (a panic here would crash the test; -race guards the
// lock discipline).
func TestSlice4_W7_ConcurrentEnqueueAndStop(t *testing.T) {
	q := testQueue(Config{Capacity: 64, MaxBytes: 1 << 20, Workers: 4})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 64 {
				q.TryEnqueue(func(context.Context) error { return nil }, 1)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		q.Stop()
	}()
	wg.Wait()

	// Stop is idempotent.
	q.Stop()
	if err := waitFor(t, q, 2*time.Second); err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
}

// TestSlice4_W8_JobPanicRecovered proves a panicking job is recovered: the
// worker logs the panic and keeps processing, so the process does not crash and
// the row is left for the boot reconcile.
func TestSlice4_W8_JobPanicRecovered(t *testing.T) {
	var logs bytes.Buffer
	q := New(Config{Capacity: 4, MaxBytes: 1 << 20, Workers: 1}, slog.New(slog.NewTextHandler(&logs, nil)))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q.Start(ctx)

	processed := make(chan struct{})
	if !q.TryEnqueue(func(context.Context) error { panic("synthetic boom") }, 1) {
		t.Fatal("TryEnqueue(panicking job) = false, want true")
	}
	if !q.TryEnqueue(func(context.Context) error { close(processed); return nil }, 1) {
		t.Fatal("TryEnqueue(follow-up job) = false, want true")
	}

	q.Stop()
	if err := waitFor(t, q, 2*time.Second); err != nil {
		t.Fatalf("Wait = %v, want nil (the worker must survive the panic)", err)
	}

	select {
	case <-processed:
	default:
		t.Fatal("the worker did not process the job after a panic")
	}
	if !strings.Contains(logs.String(), "job panicked") || !strings.Contains(logs.String(), "synthetic boom") {
		t.Errorf("panic was not logged with details: %s", logs.String())
	}
	if q.Depth() != 0 {
		t.Errorf("Depth = %d, want 0 (all jobs released)", q.Depth())
	}
}

// waitFor runs q.Wait under a bounded context.
func waitFor(t *testing.T, q *Queue, d time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return q.Wait(ctx)
}
