<?php

/*
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

namespace OPNsense\WireguardQuic;

class UiControllerBase extends \OPNsense\Base\IndexController
{
    protected function templateJSIncludes()
    {
        $result = parent::templateJSIncludes();
        $result[] = '/ui/js/wg-quic/page.js';
        return $result;
    }

    protected function templateCSSIncludes()
    {
        $result = parent::templateCSSIncludes();
        $result[] = '/ui/css/wg-quic.css';
        return $result;
    }

    public function beforeExecuteRoute($dispatcher)
    {
        $result = parent::beforeExecuteRoute($dispatcher);
        if ($result !== false) {
            $translator = new Translator($this->langcode, $this->translator);
            $this->view->setVar('lang', $translator);
            $this->localizeMenu($this->view->menuSystem, $translator);
        }
        return $result;
    }

    public function getForm($formname)
    {
        return (new Translator($this->langcode))->form(parent::getForm($formname), $this->formVocabulary($formname));
    }

    public function getFormGrid($formname, $grid_id = null, $root = null)
    {
        return (new Translator($this->langcode))->form(
            parent::getFormGrid($formname, $grid_id, $root),
            $this->formVocabulary($formname, true)
        );
    }

    private function localizeMenu($items, $translator)
    {
        $labels = [
            '/ui/wireguardquic/general#instances' => 'Instances',
            '/ui/wireguardquic/general#peers' => 'Peers',
            '/ui/wireguardquic/general#configbuilder' => 'Peer generator'
        ];
        foreach ($items as $item) {
            if (isset($labels[$item->Url])) {
                $item->VisibleName = $translator->text($labels[$item->Url]);
            }
            $this->localizeMenu($item->Children, $translator);
        }
    }

    private function formVocabulary($formname, $grid = false)
    {
        // Read source labels before core gettext can collapse distinct labels
        // (for example Address and Endpoint address) into the same translation.
        $xml = simplexml_load_file(__DIR__ . '/forms/' . $formname . '.xml');
        $source = [];
        foreach ($xml->xpath('//field[id]') as $field) {
            $id = (string)$field->id;
            if ($grid) {
                $parts = explode('.', $id);
                $id = (string)($field->grid_view->{'column-id'} ?? end($parts));
            }
            foreach ($grid ? ['label'] : ['label', 'help', 'hint'] as $key) {
                if (isset($field->$key)) {
                    $source[$id][$key] = (string)($grid ? $field->grid_view->$key ?? $field->$key : $field->$key);
                }
            }
        }
        return $source;
    }
}
