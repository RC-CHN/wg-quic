<?php

/*
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

function wireguardquic_enabled()
{
    return (string)(new \OPNsense\WireguardQuic\General())->enabled === '1';
}

function wireguardquic_services()
{
    if (!wireguardquic_enabled()) {
        return [];
    }

    $services = [];
    foreach ((new \OPNsense\WireguardQuic\Server())->servers->server->iterateItems() as $key => $node) {
        if ((string)$node->enabled === '1') {
            $services[] = [
                'description' => 'wg-quic ' . htmlspecialchars((string)$node->name),
                'configd' => [
                    'restart' => ["wireguardquic restart {$key}"],
                    'start' => ["wireguardquic start {$key}"],
                    'stop' => ["wireguardquic stop {$key}"],
                ],
                'name' => 'wireguardquic',
                'nocheck' => true,
                'id' => $key,
            ];
        }
    }
    return $services;
}

function wireguardquic_devices()
{
    $names = [];
    foreach ((new \OPNsense\WireguardQuic\Server())->servers->server->iterateItems() as $node) {
        if ((string)$node->enabled === '1') {
            $interface = (string)$node->interface;
            $names[$interface] = [
                'descr' => sprintf(
                    '%s (wg-quic - %s)',
                    $interface,
                    (string)$node->name
                ),
                'ifdescr' => (string)$node->name,
                'name' => $interface,
            ];
        }
    }

    return [[
        'configurable' => false,
        'pattern' => '^quic[0-9]{1,3}$',
        'type' => 'wireguardquic',
        'volatile' => true,
        'names' => $names,
    ]];
}

function wireguardquic_interfaces()
{
    if (!wireguardquic_enabled()) {
        return [];
    }

    return [
        'wireguardquic' => [
            'descr' => gettext('wg-quic (Group)'),
            'if' => 'wireguardquic',
            'virtual' => true,
            'enable' => true,
            'type' => 'group',
            'networks' => [],
        ],
    ];
}

function wireguardquic_configure()
{
    return [
        'newwanip' => ['wireguardquic_configure_do'],
        'vpn' => ['wireguardquic_configure_do'],
    ];
}

function wireguardquic_configure_do($verbose = false)
{
    service_log('Configuring wg-quic VPN...', $verbose);
    configd_run('wireguardquic configure');
    service_log("done.\n", $verbose);
}

function wireguardquic_syslog()
{
    return [
        'wireguardquic' => ['facility' => ['wg-quic']],
    ];
}

function wireguardquic_xmlrpc_sync()
{
    return [[
        'id' => 'wireguardquic',
        'section' => 'OPNsense.wireguardquic',
        'description' => gettext('wg-quic'),
        'services' => ['wireguardquic'],
    ]];
}
