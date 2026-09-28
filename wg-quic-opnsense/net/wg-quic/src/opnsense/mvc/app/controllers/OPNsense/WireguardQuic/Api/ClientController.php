<?php

/*
 * Copyright (C) 2018 Michael Muenz <m.muenz@gmail.com>
 * Copyright (C) 2023 Deciso B.V.
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: BSD-2-Clause
 */

namespace OPNsense\WireguardQuic\Api;

use OPNsense\Base\ApiMutableModelControllerBase;
use OPNsense\Core\Backend;
use OPNsense\Core\Config;
use OPNsense\Firewall\Util;
use OPNsense\WireguardQuic\Server;

class ClientController extends ApiMutableModelControllerBase
{
    use \OPNsense\WireguardQuic\LocalizedApi;

    protected static $internalModelName = 'client';
    protected static $internalModelClass = '\OPNsense\WireguardQuic\Client';

    public function pskAction()
    {
        $payload = json_decode(trim((new Backend())->configdRun('wireguardquic psk')), true);
        return [
            'psk' => is_array($payload) ? ($payload['presharedKey'] ?? '') : '',
            'status' => is_array($payload) && ($payload['status'] ?? '') === 'ok' ? 'ok' : 'failed',
        ];
    }

    public function listServersAction()
    {
        if (!$this->request->isGet()) {
            return ['status' => 'failed'];
        }
        $results = ['rows' => [], 'status' => 'ok'];
        foreach ((new Server())->servers->server->iterateItems() as $key => $node) {
            $results['rows'][] = [
                'uuid' => $key,
                'name' => (string)$node->name,
                'interface' => (string)$node->interface,
                'enabled' => (string)$node->enabled === '1',
                'congestion' => (string)$node->congestion,
                'fec' => (string)$node->fec,
                'obfs' => (string)$node->obfs,
            ];
        }
        return $results;
    }

    public function searchClientAction()
    {
        $servers = $this->request->get('servers');
        $filter = function ($record) use ($servers) {
            return empty($servers) || array_intersect(explode(',', $record->servers), $servers);
        };
        return $this->searchBase('clients.client', null, null, $filter);
    }

    public function getClientAction($uuid = null)
    {
        return $this->getBase('client', 'clients.client', $uuid);
    }

    public function addClientAction()
    {
        return $this->setClientAction(null);
    }

    public function delClientAction($uuid)
    {
        if ($this->request->isPost()) {
            Config::getInstance()->lock();
            $model = new Server();
            foreach ($model->servers->server->iterateItems() as $node) {
                $peers = array_filter(explode(',', (string)$node->peers));
                if (in_array($uuid, $peers)) {
                    $node->peers = implode(',', array_diff($peers, [$uuid]));
                }
            }
            $model->serializeToConfig(false, true);
        }
        return $this->delBase('clients.client', $uuid);
    }

    public function setClientAction($uuid)
    {
        // These values are owned by the instance, including when a peer is
        // attached to several instances. Never accept peer-level overrides.
        $client = $this->request->getPost('client');
        if (is_array($client)) {
            foreach (['congestion', 'fec', 'obfs'] as $field) {
                if (array_key_exists($field, $client)) {
                    return ['result' => 'failed', 'validations' => [
                        'client.servers' => gettext('Transport settings belong to the instance. Edit the instance instead.'),
                    ]];
                }
            }
        }
        $addedUuid = null;
        if ($this->request->isPost() && is_array($client) && empty($uuid)) {
            $uuid = $this->getModel()->clients->generateUUID();
            $addedUuid = $uuid;
        }
        $result = $this->setBase('client', 'clients.client', $uuid);
        if ($addedUuid !== null && ($result['result'] ?? '') === 'saved') {
            $result['uuid'] = $addedUuid;
        }
        return $result;
    }

    protected function setBaseHook($node)
    {
        // The framework calls this only after validation, under the config
        // lock, immediately before the single durable save of both models.
        $input = $this->request->getPost('client');
        $builder = $this->request->getPost('configbuilder');
        if (is_array($builder)) {
            $servers = [$builder['server']];
        } elseif (is_array($input) && array_key_exists('servers', $input)) {
            $servers = array_filter(explode(',', (string)$input['servers']));
        } else {
            return; // A partial update must preserve existing memberships.
        }
        $uuid = $node->getAttribute('uuid');
        $model = new Server();
        foreach ($model->servers->server->iterateItems() as $key => $server) {
            $peers = array_filter(explode(',', (string)$server->peers));
            $peers = array_values(array_diff($peers, [$uuid]));
            if (in_array($key, $servers, true)) {
                $peers[] = $uuid;
            }
            $server->peers = implode(',', $peers);
        }
        $model->serializeToConfig(false, true);
    }

    public function toggleClientAction($uuid)
    {
        return $this->toggleBase('clients.client', $uuid);
    }

    public function getClientBuilderAction()
    {
        return $this->getBase('configbuilder', 'clients.client', null);
    }

    public function addClientBuilderAction()
    {
        $input = $this->request->getPost('configbuilder');
        if (!$this->request->isPost() || !is_array($input)) {
            return ['result' => 'failed'];
        }
        Config::getInstance()->lock();
        $serverId = $input['server'] ?? '';
        if (!is_string($serverId) || !preg_match('/^[0-9a-f-]{36}$/i', $serverId) ||
            (new Server())->getNodeByReference('servers.server.' . $serverId) === null) {
            return ['result' => 'failed', 'validations' => [
                'configbuilder.servers' => gettext('Select an existing instance before generating a peer.'),
            ]];
        }
        $server = (new Server())->getNodeByReference('servers.server.' . $serverId);
        if (isset($input['revision']) && (!is_string($input['revision']) ||
            !hash_equals($this->builderRevision($server), $input['revision']))) {
            return ['result' => 'failed', 'validations' => [
                'configbuilder.servers' => gettext('Instance settings changed. Select the instance again to refresh the profile before saving.'),
            ]];
        }
        $uuid = $this->getModel()->clients->generateUUID();
        $result = $this->setBase('configbuilder', 'clients.client', $uuid);
        if (($result['result'] ?? '') === 'saved') {
            $result['uuid'] = $uuid;
        }
        return $result;
    }

    private function builderRevision($server)
    {
        $values = [];
        foreach (['pubkey', 'endpoint', 'peer_dns', 'mtu', 'congestion', 'fec', 'obfs', 'tunneladdress', 'peers'] as $key) {
            $values[$key] = (string)$server->$key;
        }
        return hash('sha256', json_encode($values));
    }

    public function getServerInfoAction($uuid = null)
    {
        $result = ['status' => 'failed'];
        if (!$this->request->isGet()) {
            return $result;
        }

        foreach ((new Server())->servers->server->iterateItems() as $key => $node) {
            if ($key !== $uuid) {
                continue;
            }
            $result['endpoint'] = (string)$node->endpoint;
            $result['peer_dns'] = (string)$node->peer_dns;
            $result['mtu'] = (string)$node->mtu;
            $result['pubkey'] = (string)$node->pubkey;
            $result['congestion'] = (string)$node->congestion;
            $result['fec'] = (string)$node->fec;
            $result['obfs'] = (string)$node->obfs;
            $result['revision'] = $this->builderRevision($node);
            $subnets = [];
            $usedAddresses = [];

            foreach (array_filter(explode(',', (string)$node->tunneladdress)) as $address) {
                $protocol = str_contains($address, ':') ? 'inet6' : 'inet';
                $subnets[$protocol] ??= $address;
                $packed = @inet_pton(explode('/', $address)[0]);
                if ($packed !== false) {
                    $usedAddresses[] = inet_ntop($packed);
                }
            }
            foreach (array_filter(explode(',', (string)$node->peers)) as $peerUuid) {
                $peer = $this->getModel()->getNodeByReference('clients.client.' . $peerUuid);
                if ($peer === null) {
                    continue;
                }
                foreach (array_filter(explode(',', (string)$peer->tunneladdress)) as $address) {
                    $packed = @inet_pton(explode('/', $address)[0]);
                    if ($packed !== false) {
                        $usedAddresses[] = inet_ntop($packed);
                    }
                }
            }

            $addresses = [];
            foreach ($subnets as $cidr) {
                foreach (Util::cidrRangeIterator($cidr) as $address) {
                    if (!in_array($address, $usedAddresses)) {
                        $addresses[] = $address . (str_contains($address, ':') ? '/128' : '/32');
                        break;
                    }
                }
            }
            $result['address'] = implode(',', $addresses);
            $result['status'] = 'ok';
            break;
        }
        return $result;
    }
}
