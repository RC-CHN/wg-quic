# Imported behavior tests

The initial fork imported all 23 upstream test artifacts from revision
`ecfc5a8d5446`. They are maintained and versioned in this repository:

- `conn/bind_std_test.go`
- `conn/conn_test.go`
- `conn/sticky_linux_test.go`
- `device/allowedips_rand_test.go`
- `device/allowedips_test.go`
- `device/bind_test.go`
- `device/cookie_test.go`
- `device/device_test.go`
- `device/endpoint_test.go`
- `device/kdf_test.go`
- `device/noise_test.go`
- `device/pools_test.go`
- `device/race_disabled_test.go`
- `device/race_enabled_test.go`
- `format_test.go`
- `ipc/namedpipe/namedpipe_test.go`
- `ratelimiter/ratelimiter_test.go`
- `replay/replay_test.go`
- `tai64n/tai64n_test.go`
- `tests/netns.sh`
- `tun/alignment_windows_test.go`
- `tun/checksum_test.go`
- `tun/offload_linux_test.go`

Go tests run through the root module on every supported CI operating system.
The privileged `netns.sh` semantics are mapped to the wg-quic carrier in
`tests/WIREGUARD-FORK.md`.
