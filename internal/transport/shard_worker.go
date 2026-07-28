// shard_worker.go — per-shard worker pool shared between TCP and QUIC.
//
// Each connection (TCP) or stream (QUIC) is dispatched to the shard
// worker that owns its key.  The shard worker drains its queue
// serially, so the shard's CacheService is touched by exactly one
// goroutine at a time.  This eliminates the data race that would
// otherwise occur when two connections land on the same shard.
package transport

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
)

// Job is a unit of work routed to a shard worker.
type Job struct {
	// Conn is the TCP connection or QUIC stream wrapped as net.Conn
	// (the QUIC handler uses a stream adapter — see quic/server.go).
	Conn net.Conn
	// Handle runs the job.  The worker invokes it on the shard
	// goroutine, so the handler is single-threaded by construction.
	Handle func(Job)
}

// shardWorker processes one shard's jobs serially.
type shardWorker struct {
	id    int
	queue chan Job
	stop  chan struct{}
	wg    sync.WaitGroup
	done  atomic.Bool
}

func newShardWorker(id, queueDepth int) *shardWorker {
	if queueDepth <= 0 {
		queueDepth = 4096
	}
	return &shardWorker{
		id:    id,
		queue: make(chan Job, queueDepth),
		stop:  make(chan struct{}),
	}
}

func (w *shardWorker) start() {
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for {
			select {
			case <-w.stop:
				for {
					select {
					case j := <-w.queue:
						closeJob(j)
					default:
						return
					}
				}
			case j := <-w.queue:
				j.Handle(j)
			}
		}
	}()
}

func (w *shardWorker) submit(j Job) bool {
	if w.done.Load() {
		closeJob(j)
		return false
	}
	select {
	case w.queue <- j:
		return true
	case <-w.stop:
		closeJob(j)
		return false
	}
}

func (w *shardWorker) shutdown(ctx context.Context) {
	if !w.done.CompareAndSwap(false, true) {
		return
	}
	close(w.stop)
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func closeJob(j Job) {
	if j.Conn != nil {
		_ = j.Conn.Close()
	}
}

// WorkerPool is a set of shardWorkers indexed by shard id.
type WorkerPool struct {
	workers []*shardWorker
}

// NewWorkerPool constructs a pool of numWorkers workers, each with
// queueDepth slots in its job queue.
func NewWorkerPool(numWorkers, queueDepth int) *WorkerPool {
	p := &WorkerPool{workers: make([]*shardWorker, numWorkers)}
	for i := 0; i < numWorkers; i++ {
		p.workers[i] = newShardWorker(i, queueDepth)
	}
	return p
}

// ShardCount returns the number of workers (= number of shards).
func (p *WorkerPool) ShardCount() int { return len(p.workers) }

// Submit routes a job to the worker whose index matches shardIdx.
// Returns false if the worker has stopped.
func (p *WorkerPool) Submit(j Job, shardIdx int) bool {
	if shardIdx < 0 || shardIdx >= len(p.workers) {
		closeJob(j)
		return false
	}
	return p.workers[shardIdx].submit(j)
}

// Start launches every worker.
func (p *WorkerPool) Start() {
	for _, w := range p.workers {
		w.start()
	}
}

// Shutdown stops every worker, bounded by ctx.
func (p *WorkerPool) Shutdown(ctx context.Context) {
	for _, w := range p.workers {
		w.shutdown(ctx)
	}
}
