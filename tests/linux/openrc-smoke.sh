#!/bin/sh
set -eu

repo_dir=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)

docker run --rm \
	-v "$repo_dir:/source:ro" \
	alpine:3.22 \
	/bin/sh -eu -c '
		apk add --no-cache openrc >/dev/null
		mkdir -p /run/openrc /etc/wg-quic /usr/local/bin
		touch /run/openrc/softlevel
		install -m 0755 /source/packaging/openrc/wg-quic /etc/init.d/wg-quic
		ln -s wg-quic /etc/init.d/wg-quic.wg0
		install -m 0755 /source/tests/linux/fixtures/fake-wg-quic-quick /usr/local/bin/wg-quic-quick
		install -m 0600 /dev/null /etc/wg-quic/wg0.conf

		/etc/init.d/wg-quic.wg0 start
		attempt=0
		while ! grep -Fxq run:wg0 /tmp/wg-quic-openrc-events; do
			attempt=$((attempt + 1))
			[ "$attempt" -lt 50 ]
			sleep 0.1
		done
		/etc/init.d/wg-quic.wg0 reload
		grep -Fxq check:wg0 /tmp/wg-quic-openrc-events
		grep -Fxq reload:wg0 /tmp/wg-quic-openrc-events
		/etc/init.d/wg-quic.wg0 stop
		if /etc/init.d/wg-quic.wg0 status >/dev/null 2>&1; then
			exit 1
		fi
	'
