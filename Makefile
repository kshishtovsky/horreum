.PHONY: test bench soak vet fuzz clean

test:
	go test -race ./internal/...

bench:
	go test -bench=. -benchmem ./internal/arena/...
	go test -bench=. -benchmem ./internal/index/...

soak:
	go run ./cmd/horreum-bench/ --soak --rate=200000 --sizedist=pareto:4096:16384 --duration=1000000

vet:
	go vet ./...

fuzz:
	go test -fuzz=FuzzArenaPut ./internal/arena/ -fuzztime=60s

clean:
	rm -f bench/phase1.txt
