# quic-go origin

This directory contains the complete source and tests of:

- upstream: `https://github.com/quic-go/quic-go`
- module: `github.com/quic-go/quic-go`
- version: `v0.61.0`
- module checksum:
  `h1:ui88A53s8MSVYLC56en0KQ17HARk+9986Dn0SBfKNvA=`
- module file checksum:
  `h1:9So2anK4Tp22URSQq00k+Vo2PNkle96ycDPDHL4s9vs=`

The source was imported from the Go module proxy cache without modifying the
upstream module path. The root module uses an explicit local `replace`, so
wg-quic production builds cannot select a quic-go implementation outside this
repository.

Local changes must be made in this directory, retain the upstream MIT license,
and include tests in this module. CI runs both this module's own test suite and
the root wg-quic integration tests.

The owned DATAGRAM send pool stores fixed-size array pointers so returning a
buffer does not allocate a slice header. The ownership and capacity checks are
unchanged; pool lifecycle tests and benchmarks cover the local change.

Controller event snapshots stay on the stack until a transition is recorded.
Event history still retains immutable before/after values; regression tests
cover ordinary observations and subsequent transitions.

The send queue coalesces already-queued equal-size single-segment packets into
bounded GSO writes without padding or waiting. Tests cover datagram order and
contents, ECN boundaries, disabled GSO, existing batches, buffer capacity,
shutdown, write errors, and segment-sized MTU feedback.

The local model controller requires fresh delivery measurements before
counting startup plateau rounds. Its congestion-window RTT budget includes
pacing timer granularity on sub-millisecond paths; minimum-rate calculations
use the same budget so ECN and queued loss still reduce the window. Propagation
RTT telemetry and the existing maximum congestion window remain unchanged.

A full DATAGRAM receive queue yields once to runnable consumers before one
enqueue retry. Persistent overload still drops packets; queue capacity does
not grow. Closing the queue synchronizes rejection of subsequent enqueues.
Tests cover consumer progress, stalled-consumer drops, and closed queues.
