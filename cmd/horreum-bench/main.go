// Command horreum-bench runs soak tests against the arena allocator.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/horreum/horreum/internal/arena"
)

func main() {
	soak := flag.Bool("soak", false, "run soak test with mixed SET/DEL")
	rate := flag.Int("rate", 200000, "ops per second target (soak mode)")
	sizeDist := flag.String("sizedist", "pareto:4096:16384", "size distribution: uniform|min:max or pareto:mean:max")
	duration := flag.Int("duration", 1000000, "total operations (soak mode)")
	regionSize := flag.Uint64("region-size", 1<<30, "region size in bytes (default 1 GiB)")
	flag.Parse()

	if *soak {
		runSoak(*rate, *sizeDist, *duration, *regionSize)
	} else {
		runQuickBench(*regionSize)
	}
}

func runQuickBench(regionSize uint64) {
	m, err := arena.NewManager(regionSize, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewManager: %v\n", err)
		os.Exit(1)
	}
	defer m.Close()

	sizes := []int{64, 1024, 65536, 1048576}
	for _, sz := range sizes {
		data := make([]byte, sz)
		for i := range data {
			data[i] = byte(i)
		}

		start := time.Now()
		var handles []arena.Handle
		for i := 0; i < 10000; i++ {
			h, err := m.Put(data)
			if err != nil {
				break
			}
			handles = append(handles, h)
		}
		putDur := time.Since(start)

		start = time.Now()
		for _, h := range handles {
			m.View(h)
		}
		viewDur := time.Since(start)

		fmt.Printf("size=%6d  put=%10s  view=%10s  ops=%d\n", sz, putDur, viewDur, len(handles))
	}

	s := m.Stats()
	fmt.Printf("\nStats: used=%d free=%d live=%d frag=%.4f\n",
		s.UsedBytes, s.FreeBytes, s.LiveObjects, s.Fragmentation)
}

func runSoak(rate int, sizeDist string, totalOps int, regionSize uint64) {
	m, err := arena.NewManager(regionSize, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewManager: %v\n", err)
		os.Exit(1)
	}

	dist, mean, max := parseSizeDist(sizeDist)
	_ = mean

	const goroutines = 8
	opsPerGoroutine := totalOps / goroutines

	var (
		totalOpsDone  atomic.Int64
		totalErrors   atomic.Int64
		totalLiveSize atomic.Uint64 // actual sum of live handle sizes
	)

	// VmRSS logger.
	stopLog := make(chan struct{})
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopLog:
				return
			case <-ticker.C:
				s := m.Stats()
				rss := getVmRSS()
				fmt.Printf("[soak] ops=%d live=%d used=%d free=%d frag=%.4f rss=%dMB livemb=%d\n",
					totalOpsDone.Load(), s.LiveObjects, s.UsedBytes, s.FreeBytes,
					s.Fragmentation, rss/(1024*1024), totalLiveSize.Load()/(1024*1024))
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(goroutines)
	start := time.Now()

	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(id)))
			type entry struct {
				h  arena.Handle
				sz uint32
			}
			handles := make([]entry, 0, 1000)

			for i := 0; i < opsPerGoroutine; i++ {
				sz := dist(rng, max)
				data := make([]byte, sz)
				rng.Read(data)

				h, err := m.Put(data)
				if err != nil {
					if err == arena.ErrArenaFull {
						continue
					}
					totalErrors.Add(1)
					continue
				}
				handles = append(handles, entry{h, uint32(sz)})
				totalOpsDone.Add(1)
				totalLiveSize.Add(uint64(sz))

				// Simulate DEL: free ~10% of recently inserted.
				if len(handles) > 0 && rng.Intn(10) == 0 {
					idx := rng.Intn(len(handles))
					m.Free(handles[idx].h)
					totalLiveSize.Add(^uint64(handles[idx].sz) + 1) // subtract
					handles = append(handles[:idx], handles[idx+1:]...)
				}
			}
		}(g)
	}

	wg.Wait()
	close(stopLog)
	elapsed := time.Since(start)

	s := m.Stats()
	liveBytes := totalLiveSize.Load()
	rssBefore := getVmRSS()

	fmt.Printf("\n=== Soak Results ===\n")
	fmt.Printf("Total ops:   %d\n", totalOpsDone.Load())
	fmt.Printf("Errors:      %d\n", totalErrors.Load())
	fmt.Printf("Duration:    %s\n", elapsed)
	fmt.Printf("Throughput:  %.0f ops/s\n", float64(totalOpsDone.Load())/elapsed.Seconds())
	fmt.Printf("Live objects: %d\n", s.LiveObjects)
	fmt.Printf("Used bytes:  %d (bump offset)\n", s.UsedBytes)
	fmt.Printf("Live bytes:  %d (actual handle sizes)\n", liveBytes)
	fmt.Printf("Free bytes:  %d\n", s.FreeBytes)
	fmt.Printf("Fragmentation: %.4f\n", s.Fragmentation)
	fmt.Printf("VmRSS before Close: %dMB\n", rssBefore/(1024*1024))

	m.Close()
	// Force GC to release Go-side allocations from the test.
	runtime.GC()
	runtime.GC()
	rssAfter := getVmRSS()

	fmt.Printf("VmRSS after Close:  %dMB\n", rssAfter/(1024*1024))
	fmt.Printf("RSS freed:    %dMB\n", (rssBefore-rssAfter)/(1024*1024))

	if liveBytes > 0 {
		fmt.Printf("RSS/live ratio: %.2f (RSS before close / live bytes)\n",
			float64(rssBefore)/float64(liveBytes))
	}

	if s.Fragmentation >= 0.01 {
		fmt.Printf("FAIL: fragmentation %.4f >= 0.01\n", s.Fragmentation)
		os.Exit(1)
	}
}

func parseSizeDist(s string) (func(*rand.Rand, int) int, int, int) {
	parts := strings.Split(s, ":")
	if len(parts) < 3 {
		// Default to pareto 4096:16384.
		parts = []string{"pareto", "4096", "16384"}
	}
	kind := parts[0]
	a, _ := strconv.Atoi(parts[1])
	b, _ := strconv.Atoi(parts[2])
	switch kind {
	case "uniform":
		return func(rng *rand.Rand, max int) int {
			return a + rng.Intn(b-a+1)
		}, a, b
	default: // pareto
		mean := a
		maxSize := b
		return func(rng *rand.Rand, max int) int {
			u := rng.Float64()
			if u >= 1.0 {
				u = 0.999
			}
			alpha := 1.5
			val := float64(mean) / math.Pow(1.0-u, 1.0/alpha)
			if val > float64(maxSize) {
				val = float64(maxSize)
			}
			if val < 1 {
				val = 1
			}
			return int(val)
		}, mean, maxSize
	}
}

func getVmRSS() uint64 {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	return parseVmRSSFrom(bytes.NewReader(data))
}

// parseVmRSSFrom scans an /proc/self/status-like stream for "VmRSS:" and
// returns the value in bytes (the on-disk unit is kB).  Returns 0 if the
// key is missing or the value is malformed.
func parseVmRSSFrom(r io.Reader) uint64 {
	const key = "VmRSS:"
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	if n == 0 {
		return 0
	}
	data := buf[:n]
	idx := bytes.Index(data, []byte(key))
	if idx < 0 {
		return 0
	}
	j := idx + len(key)
	for j < len(data) && (data[j] == ' ' || data[j] == '\t') {
		j++
	}
	var rss uint64
	for j < len(data) && data[j] >= '0' && data[j] <= '9' {
		rss = rss*10 + uint64(data[j]-'0')
		j++
	}
	if rss == 0 {
		return 0
	}
	return rss * 1024
}

var _ = runtime.NumCPU
