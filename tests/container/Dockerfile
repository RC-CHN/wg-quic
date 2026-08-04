FROM alpine:3.21

RUN apk add --no-cache iperf3 iproute2 iptables iputils

COPY tests/container/build/wg-quic /usr/local/bin/wg-quic
COPY tests/container/build/wg-quic-quick /usr/local/bin/wg-quic-quick
COPY tests/container/entrypoint.sh /usr/local/bin/wg-quic-test-entrypoint

ENTRYPOINT ["/usr/local/bin/wg-quic-test-entrypoint"]
