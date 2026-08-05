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
        $addedUuid = null;
        if (!empty($this->request->getPost('client')) && $this->request->isPost()) {
            $servers = array_filter(explode(',', $this->request->getPost('client')['servers'] ?? ''));
            Config::getInstance()->lock();
            $model = new Server();
            if (empty($uuid)) {
                $uuid = $model->servers->generateUUID();
                $addedUuid = $uuid;
            }
            foreach ($model->servers->server->iterateItems() as $key => $node) {
                $peers = array_filter(explode(',', (string)$node->peers));
                if (in_array($uuid, $peers) && !in_array($key, $servers)) {
                    $node->peers = implode(',', array_diff($peers, [$uuid]));
                } elseif (!in_array($uuid, $peers) && in_array($key, $servers)) {
                    $node->peers = implode(',', array_merge($peers, [$uuid]));
                }
            }
            $model->serializeToConfig(false, true);
        }

        $result = $this->setBase('client', 'clients.client', $uuid);
        if (!empty($addedUuid) && ($result['result'] ?? '') === 'saved') {
            $result['uuid'] = $addedUuid;
        }
        return $result;
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
        $uuid = null;
        if ($this->request->isPost() && !empty($this->request->getPost('configbuilder'))) {
            Config::getInstance()->lock();
            $model = new Server();
            $uuid = $this->getModel()->clients->generateUUID();
            $server = $this->request->getPost('configbuilder')['server'] ?? '';
            foreach ($model->servers->server->iterateItems() as $key => $node) {
                if ($key === $server) {
                    $peers = array_filter(explode(',', (string)$node->peers));
                    $node->peers = implode(',', array_merge($peers, [$uuid]));
                    break;
                }
            }
            $model->serializeToConfig(false, true);
        }
        return $this->setBase('configbuilder', 'clients.client', $uuid);
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
