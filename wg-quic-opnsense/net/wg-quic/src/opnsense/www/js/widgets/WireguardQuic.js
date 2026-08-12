/**
 * Copyright (C) 2024 Deciso B.V.
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: BSD-2-Clause
 */

export default class WireguardQuic extends BaseTableWidget {
    getGridOptions() {
        return {
            sizeToContent: 650
        };
    }

    getMarkup() {
        const $container = $('<div></div>');
        $container.append(this.createTable('wireguardQuicTunnelTable', {
            headerPosition: 'left'
        }));
        return $container;
    }

    async onWidgetTick() {
        const settings = await this.ajaxCall('/api/wireguardquic/general/get');
        if (!settings.general || !settings.general.enabled) {
            this.displayError(this.translations.unconfigured);
            return;
        }

        const response = await this.ajaxCall('/api/wireguardquic/service/show');
        if (!response || !response.rows || response.rows.length === 0) {
            this.displayError(this.translations.notunnels);
            return;
        }
        if (!this.dataChanged('wg-quic-tunnels', response.rows)) {
            return;
        }
        this.processTunnels(response.rows);
    }

    displayError(message) {
        $('#wireguardQuicTunnelTable').empty().append(
            $('<div class="error-message"></div>')
                .append('<a href="/ui/wireguardquic/general"></a>')
                .text(message)
        );
    }

    processTunnels(rows) {
        $('.wg-quic-interface').tooltip('hide');
        const tunnels = rows
            .filter(row => row.type === 'peer')
            .map(row => ({
                if: row.if,
                name: row.name,
                allowedIps: row['allowed-ips'] || this.translations.notavailable,
                endpoint: row.endpoint || this.translations.notavailable,
                lastActivity: row['last-activity-epoch']
                    ? `${row['last-activity-epoch']} (${this.activityDirection(row)})`
                    : this.translations.notavailable,
                rx: row['transfer-rx']
                    ? this._formatBytes(row['transfer-rx'])
                    : this.translations.notavailable,
                tx: row['transfer-tx']
                    ? this._formatBytes(row['transfer-tx'])
                    : this.translations.notavailable,
                session: row.session || this.translations.notavailable,
                peerStatus: row['peer-status'],
                statusIcon: row['peer-status'] === 'online'
                    ? 'fa-check-circle fa-fw text-success'
                    : row['peer-status'] === 'stale'
                        ? 'fa-question-circle fa-fw'
                        : 'fa-times-circle fa-fw text-danger',
                statusTooltip: row['peer-status'] === 'online'
                    ? this.translations.online
                    : row['peer-status'] === 'stale'
                        ? this.translations.stale
                        : this.translations.offline,
                uniqueId: row.if + row['public-key']
            }));

        tunnels.sort((left, right) => {
            if (left.peerStatus === right.peerStatus) return 0;
            if (left.peerStatus === 'online') return -1;
            if (left.peerStatus === 'stale' && right.peerStatus !== 'online') return -1;
            return 1;
        });

        const online = tunnels.filter(tunnel => tunnel.peerStatus === 'online').length;
        const stale = tunnels.filter(tunnel => tunnel.peerStatus === 'stale').length;
        const offline = tunnels.length - online - stale;
        const summary = `
            <div>
                <span>
                    ${this.translations.online}: ${online} |
                    ${this.translations.stale}: ${stale} |
                    ${this.translations.offline}: ${offline}
                </span>
            </div>`;
        super.updateTable(
            'wireguardQuicTunnelTable',
            [[summary, '']],
            'wg-quic-summary'
        );

        tunnels.forEach(tunnel => {
            const header = `
                <div style="display: flex; justify-content: space-between; align-items: center;">
                    <div style="display: flex; align-items: center;">
                        <i class="fa ${tunnel.statusIcon} wg-quic-interface"
                            style="cursor: pointer;" data-toggle="tooltip"
                            title="${tunnel.statusTooltip}"></i>
                        &nbsp;
                        <a href="/ui/wireguardquic/general#peers&search=${encodeURIComponent(tunnel.name)}"
                            target="_blank" rel="noopener noreferrer">
                            ${tunnel.if} | ${tunnel.name}
                        </a>
                    </div>
                </div>`;
            const detail = `
                <div><span>${tunnel.allowedIps}</span></div>
                <div><span>${this.translations.endpoint}: ${tunnel.endpoint}</span></div>
                <div><span>${this.translations.lastactivity}: ${tunnel.lastActivity}</span></div>
                <div>
                    <span>${tunnel.session}</span>
                    <div style="padding-bottom: 10px;">
                        <i class="fa fa-arrow-down" style="font-size: 13px;"></i>
                        ${tunnel.rx}
                        |
                        <i class="fa fa-arrow-up" style="font-size: 13px;"></i>
                        ${tunnel.tx}
                    </div>
                </div>`;
            super.updateTable(
                'wireguardQuicTunnelTable',
                [[header, detail]],
                tunnel.uniqueId
            );
        });
        $('.wg-quic-interface').tooltip({container: 'body'});
    }

    activityDirection(row) {
        if (row['last-activity-direction'] === 'received') {
            return this.translations.received;
        }
        if (row['last-activity-direction'] === 'sent') {
            return this.translations.sent;
        }
        return this.translations.unknown;
    }
}
