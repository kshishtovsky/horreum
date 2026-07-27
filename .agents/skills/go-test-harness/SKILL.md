---
name: go-test-harness
description: Go testing patterns — table-driven, testing/quick, fuzz, -race, soak runner.
version: "1.0.0"
tags: [go, testing, fuzz, race, soak]
---

# Go Test Harness

## Table-Driven Tests

```go
tests := []struct {
    name    string
    input   []byte
    wantErr error
}{
    {"small", make([]byte, 64), nil},
    {"max", make([]byte, MaxObjectSize), nil},
    {"too_large", make([]byte, MaxObjectSize+1), ErrSizeTooLarge},
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        h, err := m.Put(tt.input)
        if err != tt.wantErr {
            t.Fatalf("Put() = %v, want %v", err, tt.wantErr)
        }
        // ...
    })
}
```

## Property-Based Testing (testing/quick)

```go
func TestArenaProperties(t *testing.T) {
    f := func(data []byte) bool {
        if len(data) == 0 || len(data) > MaxObjectSize {
            return true
        }
        h, err := m.Put(data)
        if err != nil {
            return false
        }
        got, err := m.View(h)
        if err != nil {
            return false
        }
        return bytes.Equal(got, data)
    }
    if err := quick.Check(f, &quick.Config{MaxCount: 10000}); err != nil {
        t.Error(err)
    }
}
```

## Fuzz Testing

```go
func FuzzArenaPut(f *testing.F) {
    f.Add([]byte("hello"))
    f.Add(make([]byte, 1024))
    f.Fuzz(func(t *testing.T, data []byte) {
        if len(data) > MaxObjectSize {
            return
        }
        h, err := m.Put(data)
        if err != nil {
            return
        }
        got, _ := m.View(h)
        if !bytes.Equal(got, data) {
            t.Errorf("data mismatch")
        }
    })
}
```

## Race Detector

```bash
go test -race ./internal/...
```

All tests must pass with `-race`. Use atomic operations or mutex for shared state.

## Concurrency Test Pattern

```go
func TestConcurrent(t *testing.T) {
    const goroutines = 8
    const ops = 100_000
    var wg sync.WaitGroup
    wg.Add(goroutines)
    for g := 0; g < goroutines; g++ {
        go func() {
            defer wg.Done()
            for i := 0; i < ops; i++ {
                // ... operation ...
            }
        }()
    }
    wg.Wait()
}
```

## Soak Test

Long-running test with mixed operations (SET/DEL/GET) to detect:
- Memory leaks
- Fragmentation growth
- Race conditions under sustained load

Run: `go run ./cmd/horreum-bench/ --soak --duration=1000000`
