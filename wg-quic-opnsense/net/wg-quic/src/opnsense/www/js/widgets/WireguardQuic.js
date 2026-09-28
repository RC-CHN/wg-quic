/**
 * Copyright (C) 2024 Deciso B.V.
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: BSD-2-Clause
 */

import {translator} from '../wg-quic/i18n.js';

const escapeText = (value) => $('<span/>').text(String(value ?? '')).html();

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
        if (!this.translate) {
            this.translate = await translator(document.documentElement.lang, {
                'wg-quic is currently disabled. Click to configure wg-quic.': this.translations.unconfigured,
                'No tunnels connected': this.translations.notunnels,
                Online: this.translations.online, Stale: this.translations.stale, Offline: this.translations.offline,
                'N/A': this.translations.notavailable, 'Current endpoint': this.translations.endpoint,
                'Last activity': this.translations.lastactivity, received: this.translations.received,
                sent: this.translations.sent, unknown: this.translations.unknown
            });
        }
        const settings = await this.ajaxCall('/api/wireguardquic/general/get');
        if (!settings.general || String(settings.general.enabled) !== '1') {
            this.displayError(this.translate('wg-quic is currently disabled. Click to configure wg-quic.'));
            return;
        }

        const response = await this.ajaxCall('/api/wireguardquic/service/show');
        if (!response || !response.rows || response.rows.length === 0) {
            this.displayError(this.translate('No tunnels connected'));
            return;
        }
        if (!this.dataChanged('wg-quic-tunnels', response.rows)) {
            return;
        }
        this.processTunnels(response.rows);
    }

    displayError(message) {
        delete this.cachedData['wg-quic-tunnels'];
        $('#wireguardQuicTunnelTable').empty().append(
            $('<div class="error-message"></div>')
                .append($('<a href="/ui/wireguardquic/general"></a>').text(message))
        );
    }

    processTunnels(rows) {
        $('.wg-quic-interface').tooltip('hide');
        $('#wireguardQuicTunnelTable > .error-message').remove();
        // Replace the observed set so removed peers and old warnings disappear.
        super.updateTable('wireguardQuicTunnelTable', []);
        const tunnels = rows
            .filter(row => row.type === 'peer')
            .map(row => ({
                if: row.if,
                name: row.name,
                allowedIps: row['allowed-ips'] || this.translate('N/A'),
                endpoint: row.endpoint || this.translate('N/A'),
                lastActivity: row['last-activity']
                    ? `${new Date(row['last-activity'] * 1000).toLocaleString(document.documentElement.lang, {hour12: false})} (${this.activityDirection(row)})`
                    : this.translate('N/A'),
                rx: typeof row['transfer-rx'] === 'number'
                    ? (row['transfer-rx'] === 0 ? '0 B' : this._formatBytes(row['transfer-rx']))
                    : this.translate('N/A'),
                tx: typeof row['transfer-tx'] === 'number'
                    ? (row['transfer-tx'] === 0 ? '0 B' : this._formatBytes(row['transfer-tx']))
                    : this.translate('N/A'),
                session: this.translate(({established: 'Connected', dialing: 'Connecting', reconnecting: 'Reconnecting', idle: 'Waiting', closed: 'Closed'})[row.session] || 'Unknown'),
                peerStatus: row['peer-status'],
                statusIcon: row['peer-status'] === 'online'
                    ? 'fa-check-circle fa-fw text-success'
                    : row['peer-status'] === 'stale'
                        ? 'fa-question-circle fa-fw'
                        : 'fa-times-circle fa-fw text-danger',
                statusTooltip: row['peer-status'] === 'online'
                    ? this.translate('Online')
                    : row['peer-status'] === 'stale'
                        ? this.translate('Stale')
                        : this.translate('Offline'),
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
            <div style="padding: 6px 0; font-weight: 600;">
                <span>
                    ${this.translate('Online')}: ${online} |
                    ${this.translate('Stale')}: ${stale} |
                    ${this.translate('Offline')}: ${offline}
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
                            title="${escapeText(tunnel.statusTooltip)}"></i>
                        &nbsp;
                        <a href="/ui/wireguardquic/general#peers&search=${encodeURIComponent(tunnel.name)}"
                            target="_blank" rel="noopener noreferrer">
                            ${escapeText(tunnel.if)} | ${escapeText(tunnel.name)}
                        </a>
                    </div>
                </div>`;
            const detail = `
                <div style="margin-bottom: 6px;">${this.translate('Allowed IPs')}: ${escapeText(tunnel.allowedIps)}</div>
                <div><span>${this.translate('Current endpoint')}: ${escapeText(tunnel.endpoint)}</span></div>
                <div><span>${this.translate('Last activity')}: ${escapeText(tunnel.lastActivity)}</span></div>
                <div>
                    <span>${this.translate('QUIC Session')}: ${escapeText(tunnel.session)}</span>
                    <div style="padding: 8px 0; display: flex; gap: 16px; flex-wrap: wrap;">
                        <span>${this.translate('Received')}: <strong>${escapeText(tunnel.rx)}</strong></span>
                        <span>${this.translate('Sent')}: <strong>${escapeText(tunnel.tx)}</strong></span>
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
            return this.translate('received');
        }
        if (row['last-activity-direction'] === 'sent') {
            return this.translate('sent');
        }
        return this.translate('unknown');
    }
}
