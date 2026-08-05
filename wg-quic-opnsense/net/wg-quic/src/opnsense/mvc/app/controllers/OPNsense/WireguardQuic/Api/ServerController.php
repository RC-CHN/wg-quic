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

class ServerController extends ApiMutableModelControllerBase
{
    protected static $internalModelName = 'server';
    protected static $internalModelClass = '\OPNsense\WireguardQuic\Server';

    public function keyPairAction()
    {
        $payload = json_decode(trim((new Backend())->configdRun('wireguardquic keypair')), true);
        if (!is_array($payload) || ($payload['status'] ?? '') !== 'ok') {
            return ['status' => 'failed'];
        }
        return [
            'status' => 'ok',
            'pubkey' => $payload['publicKey'],
            'privkey' => $payload['privateKey'],
        ];
    }

    public function searchServerAction()
    {
        return $this->searchBase('servers.server');
    }

    public function getServerAction($uuid = null)
    {
        return $this->getBase('server', 'servers.server', $uuid);
    }

    public function addServerAction($uuid = null)
    {
        return $this->addBase('server', 'servers.server', $uuid);
    }

    public function delServerAction($uuid)
    {
        return $this->delBase('servers.server', $uuid);
    }

    public function setServerAction($uuid = null)
    {
        return $this->setBase('server', 'servers.server', $uuid);
    }

    public function toggleServerAction($uuid)
    {
        return $this->toggleBase('servers.server', $uuid);
    }
}
