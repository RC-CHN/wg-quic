# Small VPS performance work — 2026-09-05

Target: a Linux VPS with one vCPU, 512 MiB RAM, and intermittent loss of CPU
availability. This work reduces per-packet CPU and allocation costs without
changing the wire protocol, cryptography, production queue capacities, or
congestion/FEC defaults.

## Implementation

- Salamander reuses the digest result buffer inside each direction's existing
  digest pair. The returned hint and stream remain values, so subsequent
  derivations cannot overwrite a caller's result. The repeating XOR processes
  four explicit 64-bit words per loop, removing the nested loop and redundant
  bounds checks. No CPU-specific build flags or new dependencies are required.
- The QUIC send pool stores pointers to backing arrays. Returning a sliced
  packet no longer allocates a new slice header. Existing ownership rules and
  rejection of incompatible capacities remain in place.
- Each FEC encoder reuses its own data shards and parity workspace. Output
  packets still own separate buffers. Recycled padding is explicitly cleared,
  and scratch descriptors are cleared after encoding. Payload capacity grows
  on demand and is bounded by the existing shard, frame, and interleave limits
  (at most about 544 KiB of retained data/parity workspace per encoder).
- FEC encoding uses the parity snapshot already read for epoch selection,
  avoiding a second controller read and mutex acquisition on each protected
  frame.
- The decoder tracks the earliest possible expiry and skips map scans before
  it. Completed-group reordering grace, strict expiry boundaries, late-shard
  handling, and collection size limits are unchanged. Deletions can leave an
  early deadline, causing a harmless extra scan; insertions pull it forward.
- WireGuard creates at most `min(NumCPU, GOMAXPROCS)` workers per worker class.
  This matters when a container sees all host CPUs but has a small CPU quota;
  on a VM exposing exactly one CPU, the worker count was already one.

## Microbenchmarks

Environment: Go 1.26.3, linux/amd64, Intel Xeon CPU Max 9470C, one pinned logical
CPU, `GOMAXPROCS=1`, `GOMEMLIMIT=192MiB`, default `GOAMD64=v1`. Original source
was taken from `a5f6511` using a Go build overlay. Both versions used the same
benchmark code. Original and changed executables ran in alternating order,
five samples each, 500 ms per benchmark; values below are medians.

| Operation | Before | After | Time change | Allocated bytes / operation |
|---|---:|---:|---:|---:|
| Salamander encode, 1300 B | 941.6 ns | 714.5 ns | -24.1% | 64 → 0 |
| Salamander decode, 1300 B | 893.6 ns | 687.2 ns | -23.1% | 64 → 0 |
| Repeating XOR, 1300 B | 242.1 ns | 65.9 ns | -72.8% | 0 → 0 |
| FEC encode, 8 × 1000 B + 1 parity | 3930 ns | 1958 ns | -50.2% | 10897 → 1216 |
| FEC encode, 8 × 1000 B + 2 parity | 4405 ns | 2242 ns | -49.1% | 12009 → 1264 |
| FEC encode, 8 × 1000 B + 4 parity | 5570 ns | 2918 ns | -47.6% | 14249 → 1360 |
| QUIC send buffer acquire/release | 48.83 ns | 16.64 ns | -65.9% | 24 → 0 |

FEC 8+2 allocation count fell from 33 to 11 per group. The remaining output
slice allocations are not claimed to be eliminated. Decoder recovery's
existing benchmark creates a new decoder on each iteration: it measures cold
codec setup as well as recovery and is not a steady-session recovery ceiling.

The new retained-group benchmark sends duplicates within the existing grace
window. With 128 retained groups its per-packet cost changed from 4648 ns to
44.12 ns; with 1024 groups, from 37586 ns to 49 ns. This isolates the avoided
expiry scans, not complete FEC decoding or network throughput.

A separate XOR experiment expanded the stream into a 256-byte mask and called
`crypto/subtle.XORBytes`. At 1300 B it took about 67 ns, versus about 64 ns for
the unrolled version in that comparison; at 256 B it took about 22 ns versus
13 ns. The simpler unrolled implementation was retained. This comparison does
not establish that hand-written SIMD cannot improve on it.

## Real TUN checks under resource limits

Each endpoint ran in an isolated container with 512 MiB memory, no swap,
`GOMAXPROCS=1`, and a 192 MiB Go soft memory budget. The path used symmetric
100 Mbit/s shaping, 20 ms one-way delay, 1% random loss in each direction, and
a ten-second TCP transfer with FEC and Salamander enabled. Offloads were
disabled by the existing loss fixture. Each row is one trial, not a confidence
interval or a production throughput promise.

| CPU quota per endpoint | Original goodput | Changed goodput | Sender allocated bytes | Sender GC cycles |
|---|---:|---:|---:|---:|
| 1 CPU | 58.36 Mbit/s | 61.49 Mbit/s | 313424280 → 193027944 | 18 → 10 |
| 0.5 CPU | 25.06 Mbit/s | 26.08 Mbit/s | 155103336 → 103241088 | 8 → 6 |

All four transfers completed without an interval classified as a zero-goodput
stall. Changed core RSS snapshots were about 39–41 MiB; changed whole-container
memory peaks were about 42–50 MiB. No cgroup OOM events occurred. These are
short, single-peer measurements, not bounds for long-lived or multi-peer use.

Receiver QUIC application-queue drops remain: the 1 CPU trial changed from
50 to 71, and the 0.5 CPU trial from 129 to 37. Random loss, pacing, and quota
scheduling make these short trials noisy. Queue sizing and CPU-starvation
behavior are therefore still open work; the microbenchmark gains must not be
reported as equivalent whole-tunnel throughput gains.

CPU quota throttling was verified using cgroup counters. It tests reduced CPU
availability, not actual hypervisor steal. Measure guest `%steal`, latency,
queue drops, and memory on the target VPS before tuning production defaults.
The old two-device, unlimited-input loopback benchmark can overwhelm its own
consumer on a single Go execution thread and was not used as throughput proof.

## Reproduction and validation

Microbenchmarks are in `internal/transport/obfs`, `internal/transport/fec`, and
`third_party/quic-go`. For example, on a host where CPU 2 is available:

```sh
GOMAXPROCS=1 GOMEMLIMIT=192MiB taskset -c 2 go test \
  ./internal/transport/obfs ./internal/transport/fec \
  -run '^$' -bench 'Benchmark(Salamander|EncoderGroup|DecoderRetainedGroups)' \
  -benchmem -benchtime=500ms -count=5
GOMAXPROCS=1 GOMEMLIMIT=192MiB taskset -c 2 go test \
  ./third_party/quic-go -run '^$' -bench BenchmarkDatagramSendBufferPool \
  -benchmem -benchtime=500ms -count=5
```

The [network benchmark instructions](../tests/benchmark/README.md#commands)
include CPU and memory settings. Choose a new run ID for every resource
setting. The 192 MiB budget is per Go process and is a test setting, not a
512 MiB machine-wide memory guarantee or a newly installed service default.

Validation completed: project tests, the complete in-repository quic-go test
suite, race checks for transport/bind/WireGuard device and QUIC DATAGRAM paths,
wire golden vectors, variable-length FEC recovery after workspace reuse, XOR
tail/alignment/in-place checks, expiry boundaries, ShellCheck, and CLI builds
for Linux arm64, Windows amd64, and FreeBSD amd64. Linux amd64 is the only
architecture executed in this run.

## SIMD context and remaining work

The pinned dependencies already dispatch to hardware implementations:
ChaCha20-Poly1305 and BLAKE2b have amd64 acceleration, and Reed-Solomon detects
AVX2, AVX-512/GFNI, and ARM64 NEON capabilities. The measured FEC CPU profile
contained `mulGFNI_8x2_64`; matrix arithmetic was already accelerated while
allocation, scanning, and copying remained substantial costs.

Go's explicit SIMD APIs are evolving: [Go 1.26](https://go.dev/doc/go1.26)
introduced experimental amd64 `simd/archsimd`; [Go 1.27](https://go.dev/doc/go1.27)
adds experimental portable `simd` and arm64/wasm archsimd support. This change
keeps the project's Go 1.25 source compatibility and existing runtime CPU
dispatch. Increasing GOAMD64 alone is not a replacement for measuring loops.

Next useful measurements are multi-minute VPS trials across CPU steal levels,
controlled offered-load sweeps, and receiver queue occupancy versus delay.
Further batching, buffer ownership changes in the receive path, and decoder
storage changes should follow those profiles. More parity or larger queues
can consume scarce CPU/memory and increase delay, so they are not automatic
performance improvements.
