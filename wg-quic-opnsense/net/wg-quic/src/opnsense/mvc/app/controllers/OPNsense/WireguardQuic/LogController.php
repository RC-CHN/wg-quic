<?php

/*
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

namespace OPNsense\WireguardQuic;

class LogController extends \OPNsense\Base\IndexController
{
    public function indexAction()
    {
        $this->view->pick('OPNsense/Diagnostics/log');
        $this->view->module = 'core';
        $this->view->scope = 'wireguardquic';
        $this->view->service = '';
        // Normal tunnel lifecycle and transport events are logged at Notice.
        // The generic diagnostics controller defaults unknown scopes to
        // Warning, which makes a healthy wg-quic log appear empty.
        $this->view->default_log_severity = 'Notice';
    }
}
