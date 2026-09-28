<?php

/*
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

namespace OPNsense\WireguardQuic\Api;

use OPNsense\Base\ApiMutableServiceControllerBase;
use OPNsense\Core\Backend;
use OPNsense\Core\Config;
use OPNsense\WireguardQuic\Client;
use OPNsense\WireguardQuic\Server;

class ServiceController extends ApiMutableServiceControllerBase
{
    use \OPNsense\WireguardQuic\LocalizedApi;

    protected static $internalServiceClass = '\OPNsense\WireguardQuic\General';
    protected static $internalServiceTemplate = 'OPNsense/WireguardQuic';
    protected static $internalServiceEnabled = 'enabled';
    protected static $internalServiceName = 'wireguardquic';

    private function prepareConfiguration($backend)
    {
        $backend->configdRun('interface invoke registration');
        return trim($backend->configdRun('template reload ' . escapeshellarg(static::$internalServiceTemplate))) === 'OK';
    }

    private function applyReply($output)
    {
        $payload = json_decode(trim($output), true);
        if (!is_array($payload) || !isset($payload['status'], $payload['instances'])) {
            return ['status' => 'failed', 'result' => 'failed', 'instances' => [],
                'message' => gettext('The operation result is unavailable. Check instance status before trying again.')];
        }
        return $payload;
    }

    public function reconfigureAction()
    {
        if (!$this->request->isPost()) {
            return ['status' => 'failed', 'result' => 'failed'];
        }
        Config::getInstance()->lock();
        $backend = new Backend();
        if (!$this->prepareConfiguration($backend)) {
            return ['status' => 'failed', 'result' => 'failed', 'instances' => [],
                'message' => gettext('Configuration generation failed. The saved settings have not been applied.')];
        }
        return $this->applyReply($backend->configdRun('wireguardquic web_apply'));
    }

    public function queryApplyAction($uuid)
    {
        $requestId = $this->request->getPost('request_id');
        if (!$this->request->isPost() || !preg_match('/^[0-9a-f-]{36}$/i', $uuid) ||
            !is_string($requestId) || !preg_match('/^[0-9a-f]{32}$/', $requestId)) {
            return ['status' => 'failed', 'result' => 'failed', 'instances' => []];
        }
        return $this->applyReply((new Backend())->configdpRun('wireguardquic web_query', [$uuid, $requestId]));
    }

    public function restartInstanceAction($uuid)
    {
        $token = $this->request->getPost('restart_token');
        if (!$this->request->isPost() || !preg_match('/^[0-9a-f-]{36}$/i', $uuid) ||
            !is_string($token) || !preg_match('/^[0-9a-f]{64}$/', $token)) {
            return ['status' => 'failed', 'result' => 'failed', 'instances' => []];
        }
        Config::getInstance()->lock();
        $backend = new Backend();
        if (!$this->prepareConfiguration($backend)) {
            return ['status' => 'failed', 'result' => 'failed', 'instances' => [],
                'message' => gettext('Configuration generation failed. The instance was not restarted.')];
        }
        return $this->applyReply($backend->configdpRun('wireguardquic web_restart', [$uuid, $token]));
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
            foreach (['latest-handshake', 'last-rx', 'last-tx', 'last-activity', 'next-reconnect'] as $timestamp) {
                if (!empty($record[$timestamp])) {
                    $record[$timestamp . '-age'] = max(0, time() - (int)$record[$timestamp]);
                    $record[$timestamp . '-epoch'] = date(
                        'Y-m-d H:i:s',
                        (int)$record[$timestamp]
                    );
                } else {
                    $record[$timestamp . '-age'] = null;
                    $record[$timestamp . '-epoch'] = null;
                }
            }

            if (
                $record['type'] === 'peer'
                && in_array($record['peer-status'] ?? '', ['online', 'stale', 'offline'])
            ) {
                // The backend has already classified authenticated activity;
                // keep that result distinct from the QUIC transport session.
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
