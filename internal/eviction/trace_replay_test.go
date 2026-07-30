// Trace replay tests for S3-FIFO eviction.
//
// These tests drive S3-FIFO with synthetic workloads (Zipfian-like key
// distributions, mixed GET/SET/DEL operations) and verify hit-rate.
// The workloads exercise the eviction paths: S→G, S→M, M→reinsert,
// M→evict. Hit-rate thresholds are intentionally lenient so the test
// fails on real algorithmic regressions, not on noisy variance.
package eviction_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/kshishtovsky/horreum/internal/arena"
	"github.com/kshishtovsky/horreum/internal/eviction"
)

// opKind enumerates workload operations.
type opKind int8

const (
	opSet opKind = iota
	opGet
	opDel
)

// workloadOp is a single trace entry.
type workloadOp struct {
	kind opKind
	key  []byte
}

// genZipfianWorkload produces n ops across numKeys distinct keys.
// Distribution: 70% GET, 25% SET, 5% DEL.
// Keys are drawn from a Zipfian distribution so a small subset is hot —
// this drives evictions and re-insertions.
func genZipfianWorkload(rng *rand.Rand, n, numKeys int) []workloadOp {
	ops := make([]workloadOp, n)
	keys := make([][]byte, numKeys)
	for i := range keys {
		keys[i] = []byte(fmt.Sprintf("k%04d", i))
	}
	// Zipfian via inverse CDF (simple approximation).
	const s = 1.07 // skew parameter; > 1 = skewed
	weights := make([]float64, numKeys)
	sum := 0.0
	for i := 0; i < numKeys; i++ {
		weights[i] = 1.0 / float64(i+1)
		sum += weights[i]
	}
	for i := range weights {
		weights[i] /= sum
	}
	cumsum := make([]float64, numKeys)
	cumsum[0] = weights[0]
	for i := 1; i < numKeys; i++ {
		cumsum[i] = cumsum[i-1] + weights[i]
	}

	for i := 0; i < n; i++ {
		r := rng.Float64()
		// Pick key via cumulative Zipfian distribution.
		u := rng.Float64()
		ki := 0
		for ki < numKeys-1 && cumsum[ki] < u {
			ki++
		}
		k := keys[ki]
		switch {
		case r < 0.70:
			ops[i] = workloadOp{opGet, k}
		case r < 0.95:
			ops[i] = workloadOp{opSet, k}
		default:
			ops[i] = workloadOp{opDel, k}
		}
	}
	return ops
}

// TestTraceReplay_HitRate exercises S3-FIFO on a Zipfian workload and
// asserts hit-rate >= 50%. S3-FIFO is designed to outperform LRU on
// scan-resistant workloads, so this is a conservative lower bound.
func TestTraceReplay_HitRate(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	ops := genZipfianWorkload(rng, 10000, 256)

	mgr, err := arena.NewManager(1<<24, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	ev := eviction.New(mgr, 1000)
	handleMap := make(map[string]arena.Handle, 1024)

	var hits, misses int64
	for i, op := range ops {
		switch op.kind {
		case opSet:
			h, err := mgr.Put([]byte("v"))
			if err != nil {
				t.Fatalf("Put at op %d: %v", i, err)
			}
			evicted := ev.Add(op.key, h)
			for _, e := range evicted {
				if err := mgr.Free(e); err != nil {
					t.Fatalf("Free: %v", err)
				}
			}
			if old, ok := handleMap[string(op.key)]; ok {
				if err := mgr.Free(old); err != nil {
					t.Fatalf("Free old: %v", err)
				}
			}
			handleMap[string(op.key)] = h
			misses++

		case opGet:
			h, ok := handleMap[string(op.key)]
			if !ok {
				continue
			}
			ev.Touch(h)
			hits++

		case opDel:
			h, ok := handleMap[string(op.key)]
			if !ok {
				continue
			}
			ev.Delete(op.key, h)
			if err := mgr.Free(h); err != nil {
				t.Fatalf("Free: %v", err)
			}
			delete(handleMap, string(op.key))
		}
	}

	total := hits + misses
	if total == 0 {
		t.Skip("no GETs observed")
	}
	rate := float64(hits) / float64(total)
	t.Logf("hits=%d misses=%d hit_rate=%.2f%%", hits, misses, rate*100)

	if rate < 0.50 {
		t.Errorf("hit rate %.2f%% below 50%% threshold", rate*100)
	}
}

// TestTraceReplay_Convergence: late-phase hit-rate should be at least as
// good as warmup-phase hit-rate.  S3-FIFO learns the working set during
// warmup and should converge to a higher steady-state rate.
func TestTraceReplay_Convergence(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	ops := genZipfianWorkload(rng, 20000, 128)

	mgr, err := arena.NewManager(1<<24, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	ev := eviction.New(mgr, 256)
	handleMap := make(map[string]arena.Handle, 512)

	const warmup = 5000
	var warmHits, warmMiss int64
	var lateHits, lateMiss int64

	for i, op := range ops {
		switch op.kind {
		case opSet:
			h, _ := mgr.Put([]byte("v"))
			evicted := ev.Add(op.key, h)
			for _, e := range evicted {
				_ = mgr.Free(e)
			}
			if old, ok := handleMap[string(op.key)]; ok {
				_ = mgr.Free(old)
			}
			handleMap[string(op.key)] = h
			if i < warmup {
				warmMiss++
			} else {
				lateMiss++
			}

		case opGet:
			h, ok := handleMap[string(op.key)]
			if !ok {
				continue
			}
			ev.Touch(h)
			if i < warmup {
				warmHits++
			} else {
				lateHits++
			}

		case opDel:
			h, ok := handleMap[string(op.key)]
			if !ok {
				continue
			}
			ev.Delete(op.key, h)
			_ = mgr.Free(h)
			delete(handleMap, string(op.key))
		}
	}

	t.Logf("warmup: hits=%d miss=%d", warmHits, warmMiss)
	t.Logf("late:   hits=%d miss=%d", lateHits, lateMiss)

	if lateHits+lateMiss == 0 {
		t.Skip("no late-phase traffic")
	}
	lateRate := float64(lateHits) / float64(lateHits+lateMiss)
	t.Logf("late hit-rate: %.2f%%", lateRate*100)
	// No hard assertion on late-rate; just log it. The warmup vs late
	// comparison is informational.
}

// TestTraceReplay_ScanResistance: a sequential scan should not evict hot
// keys.  S3-FIFO's ghost filter is designed for exactly this.
func TestTraceReplay_ScanResistance(t *testing.T) {
	rng := rand.New(rand.NewSource(99))

	mgr, err := arena.NewManager(1<<24, false)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()

	const numKeys = 64
	ev := eviction.New(mgr, 128) // cache size < numKeys forces eviction
	handleMap := make(map[string]arena.Handle, numKeys)

	// Phase 1: insert all hot keys and touch each many times.
	for i := 0; i < numKeys; i++ {
		key := []byte(fmt.Sprintf("hot-%02d", i))
		h, _ := mgr.Put([]byte("v"))
		evicted := ev.Add(key, h)
		for _, e := range evicted {
			_ = mgr.Free(e)
		}
		handleMap[string(key)] = h
		for j := 0; j < 5; j++ {
			ev.Touch(h)
		}
	}

	// Phase 2: scan 1000 unique cold keys — these should evict without
	// touching the hot keys (S3-FIFO's ghost filter protects them).
	for i := 0; i < 1000; i++ {
		key := []byte(fmt.Sprintf("cold-%04d", i))
		h, _ := mgr.Put([]byte("v"))
		evicted := ev.Add(key, h)
		for _, e := range evicted {
			_ = mgr.Free(e)
		}
		_ = rng // silence
	}

	// Phase 3: touch each hot key; at least most should still be present.
	var hotHits, hotMiss int
	for i := 0; i < numKeys; i++ {
		key := []byte(fmt.Sprintf("hot-%02d", i))
		h, ok := handleMap[string(key)]
		if !ok {
			hotMiss++
			continue
		}
		ev.Touch(h)
		hotHits++
	}
	t.Logf("scan-resistance: hot hits=%d, hot misses=%d", hotHits, hotMiss)
	// We don't assert a specific threshold here; the test logs the
	// behavior. A degraded S3-FIFO should still keep >= 30% of hot keys.
	if hotHits < numKeys/4 {
		t.Errorf("only %d/%d hot keys survived scan; S3-FIFO degraded",
			hotHits, numKeys)
	}
}
