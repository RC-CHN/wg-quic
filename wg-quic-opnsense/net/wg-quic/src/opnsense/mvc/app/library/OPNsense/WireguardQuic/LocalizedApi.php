<?php

/*
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

namespace OPNsense\WireguardQuic;

trait LocalizedApi
{
    public function afterExecuteRoute($dispatcher)
    {
        // External clients keep stable API responses; browser sessions use their locale.
        $data = $dispatcher->getReturnedValue();
        if (!$this->isExternalClient() && is_array($data)) {
            $translator = new Translator($this->langcode);
            $this->response->setContentType('application/json', 'UTF-8');
            $this->response->setContent($translator->response($data));
            return $this->response->send();
        }
        return parent::afterExecuteRoute($dispatcher);
    }
}
