---
name: bench-driven
description: Benchmark-driven development — go test -bench, -benchmem, benchstat, p99, -gcflags=-m.
version: "1.0.0"
tags: [go, benchmark, performance, benchstat]
---

# Benchmark-Driven Development

## Running Benchmarks

```bash
# Basic benchmark with memory stats
go test -bench=. -benchmem ./internal/arena/...

# Specific benchmark
go test -bench=BenchmarkArenaPut -benchmem ./internal/arena/...

# Multiple runs for benchstat
go test -bench=. -benchmem -count=10 ./internal/arena/... > old.txt
# ... make changes ...
go test -bench=. -benchmem -count=10 ./internal/arena/... > new.txt
benchstat old.txt new.txt
```

## Key Flags

| Flag | Purpose |
|---|---|
| `-benchmem` | Report allocs/op and bytes/op |
| `-count=N` | Run N times (for benchstat) |
| `-benchtime=X` | Run for X duration or X iterations |
| `-cpu=1,2,4` | Test with different GOMAXPROCS |

## Acceptance Criteria

For Horreum Task 01:
- `allocs/op` must be 0 for Put and View.
- `B/op` should be 0 for Put (no copies).
- Verify with `-gcflags="-m -l"` — no escapes.

## Escape Analysis

```bash
go build -gcflags="-m -l" ./internal/arena/... 2>&1 | grep "escapes to heap"
```

Must be empty for hot-path functions.

## benchstat

```bash
go install golang.org/x/perf/cmd/benchstat@latest
benchstat old.txt new.txt
```

Reports: time/op, allocs/op, B/op with confidence intervals.

## Fuzzing

```bash
go test -fuzz=FuzzArenaPut ./internal/arena/ -fuzztime=60s
```

No panics allowed.
