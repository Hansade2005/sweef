# kvbench

The harness behind the measurements in [ADR 0002](../../docs/adr/0002-ordered-kv-store-for-site-storage.md): the same
workloads against bbolt, goleveldb and Pebble. It is its own Go module (the main module does not depend on bbolt or
Pebble) and is not built by `go build ./...` at the repository root.

```
cd tools/kvbench
go build -o kvb .                       # all three engines
./run.sh leveldb fill ./kvb             # 1M sequential puts of 1000 B, then cold open, random gets, scan, fsync commits
./run.sh bbolt fillrand ./kvb           # the same with random insertion order (BBOPT=default keeps bbolt's freelist sync)
./crash.sh leveldb ./kvb 15             # kill -9 a writer 15 times and verify every batch is whole and every acknowledged one is there
for d in base bolt ldb pebble; do go build -ldflags='-s -w' -o size-$d ./size/$d; done   # binary size of each engine
```

`run.sh` drops the page cache between phases when it may (it needs root); each phase is a separate process, so the
resident set sizes it prints (`VmRSS`, `RssAnon`, `RssFile`, `VmHWM` from `/proc/self/status`) belong to that phase
alone. About 2 GB of free disk is needed.
