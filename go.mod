module github.com/RC-CHN/wg-quic

go 1.25.0

require (
	github.com/klauspost/reedsolomon v1.14.1
	github.com/quic-go/quic-go v0.61.0
	golang.org/x/crypto v0.54.0
	golang.org/x/net v0.56.0
	golang.org/x/sys v0.47.0
	golang.zx2c4.com/wintun v0.0.0-20230126152724-0fa3db229ce2
	gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c
)

replace github.com/quic-go/quic-go => ./third_party/quic-go

require (
	github.com/google/btree v1.1.2 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
	golang.org/x/time v0.7.0 // indirect
)
