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
