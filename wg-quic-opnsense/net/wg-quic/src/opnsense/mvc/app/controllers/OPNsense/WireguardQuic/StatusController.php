<?php

/*
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

namespace OPNsense\WireguardQuic;

class StatusController extends UiControllerBase
{
    public function indexAction()
    {
        $this->view->pick('OPNsense/WireguardQuic/status');
    }
}
