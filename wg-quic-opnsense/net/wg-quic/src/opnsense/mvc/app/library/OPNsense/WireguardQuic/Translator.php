<?php

/*
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

namespace OPNsense\WireguardQuic;

/** Plugin-local vocabulary; never replaces the system gettext domain. */
class Translator
{
    private $fallback;
    private $catalog = [];

    public function __construct($language, $fallback = null, $catalogPath = null)
    {
        $this->fallback = $fallback;
        // Simplified Chinese only; other locales continue to use core gettext.
        if (in_array(strtolower(str_replace('-', '_', $language)), ['zh', 'zh_cn', 'zh_hans'])) {
            $path = $catalogPath ?? '/usr/local/opnsense/www/js/wg-quic/locales/zh.json';
            $this->catalog = is_readable($path) ? (json_decode(file_get_contents($path), true) ?: []) : [];
        }
    }

    public function text($key)
    {
        return $this->catalog[$key] ?? gettext($key);
    }

    /** Match the core ViewTranslator escaping contract for Volt and partials. */
    public function _($key, array $placeholders = []): string
    {
        if (!isset($this->catalog[$key]) && $this->fallback !== null) {
            return $this->fallback->_($key, $placeholders);
        }
        $text = $this->text($key);
        foreach ($placeholders as $name => $value) {
            $text = str_replace('%' . $name . '%', $value, $text);
        }
        return view_html_safe($text);
    }

    /** Translate static form metadata only, never IDs or user configuration. */
    public function form($fields, $source = [])
    {
        $id = $fields['id'] ?? $fields['column-id'] ?? null;
        if ($id !== null && isset($source[$id])) {
            foreach ($source[$id] as $key => $value) {
                $fields[$key] = $value;
            }
        }
        foreach ($fields as $key => &$value) {
            if (is_array($value)) {
                $value = $this->form($value, $source);
            } elseif (in_array($key, ['label', 'help', 'hint', 'tab_descr'], true) && is_string($value)) {
                $value = $this->text($value);
            }
        }
        return $fields;
    }

    /** Localize presentation fields, leaving machine values and user text intact. */
    public function response($data)
    {
        if (!is_array($data)) {
            return $data;
        }
        if (isset($data['validations']) && is_array($data['validations'])) {
            foreach ($data['validations'] as &$message) {
                if (is_string($message)) {
                    $message = $this->text($message);
                }
            }
        }
        if (isset($data['message']) && is_string($data['message'])) {
            $data['message'] = $this->text($data['message']);
        }
        if (isset($data['instances']) && is_array($data['instances'])) {
            foreach ($data['instances'] as &$instance) {
                $instance = $this->response($instance);
            }
        }
        if (isset($data['restart_reasons']) && is_array($data['restart_reasons'])) {
            $data['restart_reasons'] = array_map([$this, 'text'], $data['restart_reasons']);
        }
        foreach (['server', 'client', 'configbuilder'] as $model) {
            foreach (['congestion', 'fec', 'obfs', 'fec_policy'] as $field) {
                if (isset($data[$model][$field]) && is_array($data[$model][$field])) {
                    $labels = [
                        'auto' => 'Automatic', 'model' => 'Delivery model', 'cubic' => 'CUBIC',
                        'reno' => 'Reno', 'off' => 'Disabled', 'none' => 'Disabled',
                        'salamander' => 'Salamander', 'latency' => 'Latency',
                        'balanced' => 'Balanced', 'throughput' => 'Throughput'
                    ];
                    foreach ($data[$model][$field] as $id => &$option) {
                        if (is_array($option) && isset($option['value'])) {
                            $option['value'] = $this->text($labels[$id] ?? $option['value']);
                        }
                    }
                }
            }
        }
        return $data;
    }
}
