<?php

/*
 * Copyright (C) 2023 Deciso B.V.
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: BSD-2-Clause
 */

namespace OPNsense\WireguardQuic\FieldTypes;

use OPNsense\Base\FieldTypes\ArrayField;

class ServerField extends ArrayField
{
    protected function actionPostLoadingEvent()
    {
        foreach ($this->internalChildnodes as $node) {
            if (!$node->getInternalIsVirtual()) {
                $node->cnfFilename = "/usr/local/etc/wg-quic/quic{$node->instance}.conf";
                $node->interface = "quic{$node->instance}";
            }
        }
        return parent::actionPostLoadingEvent();
    }
}
