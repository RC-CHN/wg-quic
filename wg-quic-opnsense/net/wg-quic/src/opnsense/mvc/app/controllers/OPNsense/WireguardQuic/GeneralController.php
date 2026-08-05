<?php

/*
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

namespace OPNsense\WireguardQuic;

class GeneralController extends \OPNsense\Base\IndexController
{
    protected function templateJSIncludes()
    {
        $result = parent::templateJSIncludes();
        $result[] = '/ui/js/jquery.qrcode.js';
        $result[] = '/ui/js/qrcode.js';
        return $result;
    }

    public function indexAction()
    {
        $this->view->generalForm = $this->getForm('general');
        $this->view->clientForm = $this->getForm('dialogPeer');
        $this->view->clientGrid = $this->getFormGrid('dialogPeer');
        $this->view->serverForm = $this->getForm('dialogInstance');
        $this->view->serverGrid = $this->getFormGrid('dialogInstance');
        $this->view->configBuilderForm = $this->getForm('dialogConfigBuilder');
        $this->view->pick('OPNsense/WireguardQuic/general');
    }
}
