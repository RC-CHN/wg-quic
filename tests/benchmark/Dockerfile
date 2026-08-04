FROM alpine:3.21

RUN apk add --no-cache ethtool iperf3 iproute2 iputils

COPY tests/benchmark/build/wg-quic /usr/local/bin/wg-quic
COPY tests/benchmark/build/wg-quic-quick /usr/local/bin/wg-quic-quick
COPY tests/benchmark/build/wireguard-go /usr/local/bin/wireguard-go
COPY tests/benchmark/build/wg-uapi /usr/local/bin/wg-uapi
COPY tests/benchmark/entrypoint.sh /usr/local/bin/wg-quic-benchmark-entrypoint

ENTRYPOINT ["/usr/local/bin/wg-quic-benchmark-entrypoint"]
