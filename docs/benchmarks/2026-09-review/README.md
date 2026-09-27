# September review measurements

Linux amd64, Intel Xeon Max 9470C, Go 1.26.3. Raw tool output is retained beside
this file. These are local synthetic and loopback measurements, not WAN or VPS
throughput claims. No CPU pinning or network impairment was used.

`BenchmarkEndpointStatus` compares the retained individual queries with one
indexed snapshot, including index construction. At 1,000 peers the repeated
queries take about 14 ms and the index about 0.28 ms. The index trades roughly
197 KB/request for much less scanning and locking; it is discarded after the
status request. Current-path migration, reconnect counters, closed sessions and
unknown endpoints have an equivalence regression test.

`BenchmarkDatagramReceiveRemoteAddress` enqueues and consumes a 1,200-byte
datagram with a real UDP source address. Baseline `096a554` plus the benchmark
uses `ReceiveOwned`; the new benchmark uses `ReceiveOwnedAddrPort`. Median
cost falls from 200.8 ns to 97.18 ns, and from 64 B / two allocations to zero
allocations. The existing owned API still materializes `net.UDPAddr` for its
callers. The new API captures `netip.AddrPort` by value, preserving migration,
IPv4, IPv6 and zone information without borrowing mutable input memory.

Three sequential three-second WireGuard + QUIC + Salamander loopback trials
with `GOMAXPROCS=2` measured median 427.5 Mbps before and 432.9 Mbps after.
Variation overlaps substantially. This supports retaining the allocation
improvement but does not establish a throughput improvement. The exploratory
file overlapped another benchmark and is excluded from the comparison.

Reproduction:

```sh
go test ./internal/bind -run '^TestEndpointSnapshot' -bench '^BenchmarkEndpointStatus$' -benchmem -count=3
go test ./third_party/quic-go -run '^TestDatagram' -bench '^BenchmarkDatagramReceiveRemoteAddress$' -benchmem -count=3
GOMAXPROCS=2 go test ./internal/bind -run '^$' -bench '^BenchmarkTransportThroughput/salamander$' -benchtime=3s -count=3
```

Baseline throughput uses a Go `-overlay` mapping `datagram_queue.go`,
`connection.go` and `internal/transport/quic/carrier.go` to their contents at
`096a554`, retaining the same benchmark (including the authenticated receive
callback) for both builds. Run trials sequentially. The callback is necessary
for the fixture to obey the production inbound authentication deadline.

Validation: race tests for the bind and fork's DATAGRAM queue, source mutation
after enqueue, idempotent buffer release, and endpoint snapshot equivalence.
