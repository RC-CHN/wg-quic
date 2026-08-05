<?php

/*
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

namespace OPNsense\WireguardQuic\Api;

use OPNsense\Base\ApiMutableServiceControllerBase;
use OPNsense\Core\Backend;
use OPNsense\WireguardQuic\Client;
use OPNsense\WireguardQuic\Server;

class ServiceController extends ApiMutableServiceControllerBase
{
    protected static $internalServiceClass = '\OPNsense\WireguardQuic\General';
    protected static $internalServiceTemplate = 'OPNsense/WireguardQuic';
    protected static $internalServiceEnabled = 'enabled';
    protected static $internalServiceName = 'wireguardquic';

    public function reconfigureAction()
    {
        if (!$this->request->isPost()) {
            return ['result' => 'failed'];
        }
        $backend = new Backend();
        $backend->configdRun('interface invoke registration');
        $backend->configdRun('template reload ' . escapeshellarg(static::$internalServiceTemplate));
        $backend->configdpRun('wireguardquic configure');
        return ['result' => 'ok'];
    }

    public function showAction()
    {
        $payload = json_decode((new Backend())->configdRun('wireguardquic show'), true);
        $records = !empty($payload['records']) ? $payload['records'] : [];
        $descriptions = [];
        $interfaces = [];
        $peers = [];
        foreach ((new Client())->clients->client->iterateItems() as $key => $client) {
            $peers[$key] = ['name' => (string)$client->name, 'pubkey' => (string)$client->pubkey];
        }
        foreach ((new Server())->servers->server->iterateItems() as $server) {
            $interface = (string)$server->interface;
            $descriptions[$interface . '-' . (string)$server->pubkey] = (string)$server->name;
            foreach (array_filter(explode(',', (string)$server->peers)) as $peer) {
                if (isset($peers[$peer])) {
                    $descriptions[$interface . '-' . $peers[$peer]['pubkey']] = $peers[$peer]['name'];
                }
            }
            $interfaces[$interface] = (string)$server->name;
        }

        foreach ($records as &$record) {
            $record['name'] = $descriptions[
                $record['if'] . '-' . ($record['public-key'] ?? '')
            ] ?? '';
            if (!empty($record['latest-handshake'])) {
                $record['latest-handshake-age'] = time() - (int)$record['latest-handshake'];
                $record['latest-handshake-epoch'] = date('Y-m-d H:i:s', (int)$record['latest-handshake']);
            } else {
                $record['latest-handshake-age'] = null;
                $record['latest-handshake-epoch'] = null;
            }

            if (
                $record['type'] === 'peer'
                && in_array($record['peer-status'] ?? '', ['online', 'stale', 'offline'])
            ) {
                // wg-quic exposes QUIC session state directly. Per-peer
                // WireGuard handshake timestamps are not yet part of its
                // control schema.
            } elseif ($record['type'] === 'peer' && !is_null($record['latest-handshake-age'])) {
                $record['peer-status'] = $record['latest-handshake-age'] <= 300
                    ? 'online'
                    : 'stale';
            } else {
                $record['peer-status'] = 'offline';
            }
            $record['ifname'] = $interfaces[$record['if']] ?? '';
        }
        unset($record);

        $types = $this->request->get('type');
        $filter = null;
        if (!empty($types)) {
            $filter = function ($record) use ($types) {
                return in_array($record['type'], $types);
            };
        }
        return $this->searchRecordsetBase($records, null, null, $filter);
    }

    public function versionAction()
    {
        $payload = json_decode(trim((new Backend())->configdRun('wireguardquic version')), true);
        return is_array($payload) ? $payload : ['status' => 'failed'];
    }
}
