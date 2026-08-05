<?php

/*
 * Copyright (C) 2024 Deciso B.V.
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: BSD-2-Clause
 */

namespace OPNsense\WireguardQuic\FieldTypes;

use OPNsense\Base\FieldTypes\ArrayField;
use OPNsense\WireguardQuic\Server;

class ClientField extends ArrayField
{
    protected function actionPostLoadingEvent()
    {
        $peers = [];
        foreach ((new Server())->servers->server->iterateItems() as $key => $node) {
            foreach (array_filter(explode(',', (string)$node->peers)) as $peer) {
                $peers[$peer][] = $key;
            }
        }
        foreach ($this->internalChildnodes as $key => $node) {
            if (isset($peers[$key])) {
                $node->servers->setValue(implode(',', $peers[$key]));
            }
        }
        return parent::actionPostLoadingEvent();
    }
}
