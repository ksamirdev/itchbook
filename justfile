data := "data/12302019.NASDAQ_ITCH50.gz"

default:
    @just --list

# Download one full trading day of Nasdaq TotalView-ITCH 5.0 (3.5 GB, 30 Dec 2019).
data:
    mkdir -p data
    curl --fail -C - -o {{ data }} "https://emi.nasdaq.com/ITCH/Nasdaq%20ITCH/12302019.NASDAQ_ITCH50.gz"

test:
    go test -race -count=1 ./...

# Same pinned version CI uses, built with the local Go toolchain.
lint:
    go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.6.2 run

# Compare the books against a simple model on random message streams.
fuzz time="60s":
    go test ./internal/book -run '^$' -fuzz FuzzBooksMatchModel -fuzztime {{ time }}

# Replay the whole day through the books and print the latency histogram.
bench *args:
    go run ./cmd/bench -file {{ data }} -check {{ args }}

# Profile the replay and open the flame graph.
profile limit="50000000":
    go run ./cmd/bench -file {{ data }} -limit {{ limit }} -cpuprofile cpu.pprof
    go tool pprof -http=:8081 cpu.pprof

# Send the feed over UDP on two lossy lines and rebuild the books on the other side.
feed mode="spin" loss="0.01" *args:
    go run ./cmd/feedbench -file {{ data }} -mode {{ mode }} -loss {{ loss }} {{ args }}
