#!/bin/sh

set -eu

target=${1:?usage: guest-validate.sh 26.1|26.7}
case "${target}" in
    26.1|26.7) ;;
    *) echo "unsupported target: ${target}" >&2; exit 64 ;;
esac

mount_dir=/mnt/wg-quic-share
archive="${mount_dir}/plugin-source-${target}.tar.gz"
core_archive="${mount_dir}/core-lint-${target}.tar.gz"

mkdir -p "${mount_dir}" /usr/plugins /usr/core \
    /usr/core/src/opnsense/mvc/app/models
if ! mount | grep -q "on ${mount_dir} "; then
    share_device=
    for candidate in /dev/vtbd1s1 /dev/vtbd1; do
        if [ -e "${candidate}" ]; then
            share_device="${candidate}"
            break
        fi
    done
    test -n "${share_device}"
    mount_msdosfs "${share_device}" "${mount_dir}"
fi

test -f "${archive}"
test -f "${core_archive}"
# The qcow2 disks are deliberately reusable. Remove the previous plugin
# checkout so stale work/pkg staging files can never be mistaken for a package
# built from the archive under test.
rm -rf -- /usr/plugins/net/wg-quic
tar -xzf "${archive}" -C /usr/plugins
tar -xzf "${core_archive}" -C /usr/core

echo "== platform =="
opnsense-version
freebsd-version
uname -a

echo "== plugin lint =="
cd /usr/plugins/net/wg-quic
make lint

echo "== package build =="
make package
plugin_version=$(sed -n 's/^PLUGIN_VERSION=[[:space:]]*//p' Makefile)
package_file=$(find work/pkg -name 'os-wg-quic-*.pkg' -type f | head -n 1)
test -n "${package_file}"
pkg info -F "${package_file}"
cp "${package_file}" \
    "${mount_dir}/os-wg-quic-${plugin_version}-opnsense-${target}-amd64.pkg"

echo "== package install =="
pkg add -f "${package_file}"
pkg info os-wg-quic
pkg check -s os-wg-quic

echo "== WebUI log routing =="
test -f \
    /usr/local/opnsense/service/templates/OPNsense/Syslog/local/wireguardquic.conf
logger -t wg-quic "wg-quic QEMU WebUI log routing probe"
attempt=0
while [ "${attempt}" -lt 30 ]; do
    if find /var/log/wireguardquic -name '*.log' -type f -size +0c \
        2>/dev/null | grep -q .; then
        break
    fi
    sleep 0.1
    attempt=$((attempt + 1))
done
/usr/local/opnsense/scripts/syslog/queryLog.py \
    --limit 10 \
    --offset 0 \
    --module core \
    --filename wireguardquic \
    > /tmp/wg-quic-webui-log.json
grep -q 'wg-quic QEMU WebUI log routing probe' \
    /tmp/wg-quic-webui-log.json

echo "== installed PHP syntax =="
find /usr/local/opnsense/mvc/app/controllers/OPNsense/WireguardQuic \
     /usr/local/opnsense/mvc/app/models/OPNsense/WireguardQuic \
     /usr/local/opnsense/scripts/wg-quic \
     -name '*.php' -type f -exec /usr/local/bin/php -l {} \;

echo "== registration files =="
test -f /usr/local/opnsense/mvc/app/models/OPNsense/WireguardQuic/Menu/Menu.xml
test -x /usr/local/sbin/wg-quic
test -x /usr/local/sbin/wg-quic-quick
test -f /usr/local/opnsense/www/js/widgets/WireguardQuic.js
test -f /usr/local/opnsense/www/js/widgets/Metadata/WireguardQuic.xml
test -f /usr/local/opnsense/service/conf/actions.d/actions_wireguardquic.conf
configctl wireguardquic version
configctl wireguardquic keypair > /tmp/wg-quic-keypair.json
grep -q '"status": "ok"' /tmp/wg-quic-keypair.json
grep -q '"privateKey"' /tmp/wg-quic-keypair.json
configctl wireguardquic psk > /tmp/wg-quic-psk.json
grep -q '"status": "ok"' /tmp/wg-quic-psk.json
grep -q '"presharedKey"' /tmp/wg-quic-psk.json
rm -f /tmp/wg-quic-keypair.json /tmp/wg-quic-psk.json

echo "== model and template =="
/usr/local/bin/php "${mount_dir}/configure-test.php"
configctl template reload OPNsense/WireguardQuic
test -s /usr/local/etc/wg-quic/quic0.conf
test "$(stat -f '%Lp' /usr/local/etc/wg-quic/quic0.conf)" = "600"
test "$(stat -f '%Lp' /usr/local/etc/wg-quic)" = "700"
grep -q '^\[Interface\]' /usr/local/etc/wg-quic/quic0.conf
grep -q '^\[Peer\]' /usr/local/etc/wg-quic/quic0.conf
grep -q '^Address = 10.66.0.1/24' /usr/local/etc/wg-quic/quic0.conf
grep -q '^# wg-quic: fec = auto' /usr/local/etc/wg-quic/quic0.conf
/usr/local/sbin/wg-quic-quick check /usr/local/etc/wg-quic/quic0.conf

echo "== plugin service start =="
configctl wireguardquic configure
configctl wireguardquic status
test -S /var/run/wg-quic/quic0.sock
test -S /var/run/wg-quic/quic0.sock.status
test "$(stat -f '%Lp' /var/run/wg-quic/quic0.sock)" = "600"
test "$(stat -f '%Lp' /var/run/wg-quic/quic0.sock.status)" = "666"
su -m nobody -c '/usr/local/sbin/wg-quic show quic0 --json' \
    > /tmp/wg-quic-unprivileged-status.json
jq -e '.interface == "quic0" and .state == "up"' \
    /tmp/wg-quic-unprivileged-status.json >/dev/null
rm -f /tmp/wg-quic-unprivileged-status.json
if /usr/bin/wg show all dump | grep -q '^quic0'; then
    echo "quic0 leaked into the standard WireGuard UAPI namespace" >&2
    exit 1
fi
ifconfig quic0
/usr/local/sbin/wg-quic show quic0

echo "== second userspace peer =="
mkdir -p /var/run/wg-quic
/usr/sbin/daemon -f -S -p /var/run/wg-quic/quic1.pid -T wg-quic-test \
    /usr/local/sbin/wg-quic-quick run /tmp/wg-quic-peer.conf --name quic1
attempt=0
while [ ! -S /var/run/wg-quic/quic1.sock ] && [ "${attempt}" -lt 150 ]; do
    sleep 0.1
    attempt=$((attempt + 1))
done
test -S /var/run/wg-quic/quic1.sock
test -S /var/run/wg-quic/quic1.sock.status
ifconfig quic1

echo "== userspace handshake =="
attempt=0
session0=idle
session1=idle
while [ "${attempt}" -lt 30 ]; do
    session0=$(/usr/local/sbin/wg-quic show quic0 --json | jq -r '.peers[0].session')
    session1=$(/usr/local/sbin/wg-quic show quic1 --json | jq -r '.peers[0].session')
    if [ "${session0}" = established ] && [ "${session1}" = established ]; then
        break
    fi
    sleep 1
    attempt=$((attempt + 1))
done
test "${session0}" = established
test "${session1}" = established
/usr/local/sbin/wg-quic show quic0
/usr/local/sbin/wg-quic show quic1
configctl wireguardquic show

echo "== Web API and UI =="
/usr/local/bin/php "${mount_dir}/api-credentials.php" create
api_credentials=/tmp/wg-quic-api-credentials
api_key=$(sed -n '1p' "${api_credentials}")
api_secret=$(sed -n '2p' "${api_credentials}")
api_url=https://127.0.0.1/api/wireguardquic
curl -skf -u "${api_key}:${api_secret}" \
    "${api_url}/general/get" -o /tmp/wg-quic-api-general.json
grep -q '"general"' /tmp/wg-quic-api-general.json
curl -skf -u "${api_key}:${api_secret}" \
    "${api_url}/client/search_client" -o /tmp/wg-quic-api-peers.json
grep -q '"qemu-peer"' /tmp/wg-quic-api-peers.json
curl -skf -u "${api_key}:${api_secret}" \
    "${api_url}/server/search_server" -o /tmp/wg-quic-api-servers.json
grep -q '"qemu-instance"' /tmp/wg-quic-api-servers.json
# shellcheck disable=SC2016
server_uuid=$(/usr/local/bin/php -r \
    '$d=json_decode(file_get_contents("/tmp/wg-quic-api-servers.json"),true);echo $d["rows"][0]["uuid"]??"";')
test -n "${server_uuid}"
curl -skf -u "${api_key}:${api_secret}" \
    "${api_url}/client/get_server_info/${server_uuid}" \
    -o /tmp/wg-quic-api-server-info.json
grep -q '"status":"ok"' /tmp/wg-quic-api-server-info.json
grep -q '"address":"10.66.0.' /tmp/wg-quic-api-server-info.json
curl -skf -u "${api_key}:${api_secret}" \
    "${api_url}/service/status" -o /tmp/wg-quic-api-status.json
grep -q '"status":"running"' /tmp/wg-quic-api-status.json
curl -skf -u "${api_key}:${api_secret}" \
    "${api_url}/service/show" -o /tmp/wg-quic-api-show.json
grep -q '"peer-status":"online"' /tmp/wg-quic-api-show.json
curl -skf -u "${api_key}:${api_secret}" \
    "${api_url}/service/version" -o /tmp/wg-quic-api-version.json
grep -q '"status":"ok"' /tmp/wg-quic-api-version.json
curl -skf -u "${api_key}:${api_secret}" \
    "${api_url}/server/key_pair" -o /tmp/wg-quic-api-keypair.json
grep -q '"privkey"' /tmp/wg-quic-api-keypair.json
curl -skf -u "${api_key}:${api_secret}" \
    "${api_url}/client/get_client_builder" -o /tmp/wg-quic-api-builder.json
grep -q '"configbuilder"' /tmp/wg-quic-api-builder.json
curl -skf -u "${api_key}:${api_secret}" \
    https://127.0.0.1/api/core/dashboard/get_dashboard \
    -o /tmp/wg-quic-api-dashboard.json
grep -q '"id":"WireguardQuic"' /tmp/wg-quic-api-dashboard.json

login_page=/tmp/wg-quic-login.html
login_result=/tmp/wg-quic-login-result.html
cookie_jar=/tmp/wg-quic-cookie.jar
curl -skc "${cookie_jar}" https://127.0.0.1/ -o "${login_page}"
csrf_name=$(sed -n 's/.*type="hidden" name="\([^"]*\)" value="[^"]*".*/\1/p' "${login_page}" | head -n 1)
csrf_value=$(sed -n 's/.*type="hidden" name="[^"]*" value="\([^"]*\)".*/\1/p' "${login_page}" | head -n 1)
test -n "${csrf_name}"
test -n "${csrf_value}"
curl -skb "${cookie_jar}" -c "${cookie_jar}" \
    --data-urlencode "${csrf_name}=${csrf_value}" \
    --data-urlencode "usernamefld=root" \
    --data-urlencode "passwordfld=opnsense" \
    --data-urlencode "login=1" \
    https://127.0.0.1/ -o /dev/null
curl -skb "${cookie_jar}" https://127.0.0.1/ -o "${login_result}"
grep -q 'Dashboard' "${login_result}"
curl -skb "${cookie_jar}" https://127.0.0.1/ui/wireguardquic/general \
    -o /tmp/wg-quic-ui-general.html
grep -q 'wg-quic' /tmp/wg-quic-ui-general.html
grep -q 'id="tab_instances"' /tmp/wg-quic-ui-general.html
grep -q 'id="tab_peers"' /tmp/wg-quic-ui-general.html
grep -q 'id="tab_configbuilder"' /tmp/wg-quic-ui-general.html
grep -q 'id="keygen"' /tmp/wg-quic-ui-general.html
grep -q 'id="pskgen_cb"' /tmp/wg-quic-ui-general.html
curl -skb "${cookie_jar}" https://127.0.0.1/ui/wireguardquic/status \
    -o /tmp/wg-quic-ui-status.html
grep -q 'grid-wireguardquic-status' /tmp/wg-quic-ui-status.html
grep -q 'id="type_filter"' /tmp/wg-quic-ui-status.html
curl -skb "${cookie_jar}" https://127.0.0.1/ui/wireguardquic/log \
    -o /tmp/wg-quic-ui-log.html
grep -q 'id="grid-log"' /tmp/wg-quic-ui-log.html
grep -q "let s_filter_val = 'Notice'" /tmp/wg-quic-ui-log.html
curl -skf -u "${api_key}:${api_secret}" \
    -X POST \
    --data 'rowCount=20&current=1&severity[]=Notice' \
    https://127.0.0.1/api/diagnostics/log/core/wireguardquic \
    -o /tmp/wg-quic-api-log.json
grep -q 'wg-quic QEMU WebUI log routing probe' /tmp/wg-quic-api-log.json
/usr/local/bin/php "${mount_dir}/api-credentials.php" remove
rm -f /tmp/wg-quic-api-*.json \
    "${login_page}" "${login_result}" "${cookie_jar}" \
    /tmp/wg-quic-ui-general.html /tmp/wg-quic-ui-status.html \
    /tmp/wg-quic-ui-log.html

echo "== uninstall cleanup =="
quic1_pid=$(cat /var/run/wg-quic/quic1.pid)
kill -KILL "${quic1_pid}"
attempt=0
while kill -0 "${quic1_pid}" >/dev/null 2>&1 && [ "${attempt}" -lt 50 ]; do
    sleep 0.1
    attempt=$((attempt + 1))
done
if kill -0 "${quic1_pid}" >/dev/null 2>&1; then
    echo "quic1 supervisor did not terminate" >&2
    exit 1
fi
rm -f /var/run/wg-quic/quic1.pid
/bin/timeout 30 configctl wireguardquic configure
test ! -e /var/run/wg-quic/quic1.sock
test ! -e /var/run/wg-quic/quic1.sock.status
test ! -e /var/run/wg-quic/quic1.pid
if ifconfig quic1 >/dev/null 2>&1; then
    echo "quic1 survived supervisor termination" >&2
    exit 1
fi
if pgrep -f '/usr/local/sbin/wg-quic run .* --name quic1' >/dev/null 2>&1; then
    echo "orphaned quic1 data-plane process survived supervisor termination" >&2
    exit 1
fi
configctl wireguardquic status
pkg delete -y os-wg-quic
test ! -e /usr/local/sbin/wg-quic
test ! -e /usr/local/sbin/wg-quic-quick
test ! -e /var/run/wg-quic/quic0.sock
test ! -e /var/run/wg-quic/quic0.sock.status
test ! -e /var/run/wg-quic/quic0.pid
test ! -e /var/run/wg-quic
test ! -e /usr/local/etc/wg-quic/quic0.conf
test ! -e /usr/local/opnsense/mvc/app/models/OPNsense/WireguardQuic/General.xml
test ! -e /usr/local/opnsense/mvc/app/models/OPNsense/WireguardQuic/Server.xml
test ! -e /usr/local/opnsense/mvc/app/models/OPNsense/WireguardQuic/Client.xml
test ! -e /usr/local/opnsense/mvc/app/models/OPNsense/WireguardQuic/Menu/Menu.xml
test ! -e /usr/local/opnsense/service/conf/actions.d/actions_wireguardquic.conf
test ! -e /usr/local/opnsense/www/js/widgets/WireguardQuic.js
test ! -e /usr/local/opnsense/www/js/widgets/Metadata/WireguardQuic.xml
if ifconfig quic0 >/dev/null 2>&1; then
    echo "quic0 remained after uninstall" >&2
    exit 1
fi

echo "== reinstall =="
pkg add "${package_file}"
pkg check -s os-wg-quic
configctl template reload OPNsense/WireguardQuic
configctl wireguardquic start
configctl wireguardquic status

echo "VALIDATION PASSED: OPNsense ${target}"
