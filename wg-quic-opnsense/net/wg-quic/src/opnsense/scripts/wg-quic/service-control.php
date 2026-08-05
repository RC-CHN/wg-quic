#!/usr/local/bin/php
<?php

/*
 * Copyright (C) 2023 Deciso B.V.
 * Copyright (C) 2026 wg-quic contributors
 * SPDX-License-Identifier: GPL-3.0-or-later
 */

require_once('script/load_phalcon.php');
require_once('util.inc');
require_once('config.inc');
require_once('interfaces.inc');
require_once('system.inc');

use OPNsense\WireguardQuic\General;
use OPNsense\WireguardQuic\Server;

const WG_QUIC_CORE = '/usr/local/sbin/wg-quic';
const WG_QUIC_QUICK = '/usr/local/sbin/wg-quic-quick';
const WG_QUIC_RUN_DIR = '/var/run/wg-quic';

function wireguardquic_pidfile($interface)
{
    return WG_QUIC_RUN_DIR . '/' . $interface . '.pid';
}

function wireguardquic_socket($interface)
{
    return WG_QUIC_RUN_DIR . '/' . $interface . '.sock';
}

function wireguardquic_pid($interface)
{
    $pidfile = wireguardquic_pidfile($interface);
    if (!isvalidpid($pidfile)) {
        return null;
    }
    $pid = trim((string)file_get_contents($pidfile));
    return ctype_digit($pid) && (int)$pid > 1 ? (int)$pid : null;
}

function wireguardquic_stop_interface($interface)
{
    $pid = wireguardquic_pid($interface);
    if ($pid !== null) {
        killbypid((string)$pid, 'TERM', false);
        for ($attempt = 0; $attempt < 100 && isvalidpid(wireguardquic_pidfile($interface)); $attempt++) {
            usleep(100000);
        }
        if (isvalidpid(wireguardquic_pidfile($interface))) {
            killbypid((string)$pid, 'KILL', false);
        }
    }

    @unlink(wireguardquic_pidfile($interface));
    @unlink(wireguardquic_socket($interface));
    if (does_interface_exist($interface)) {
        legacy_interface_destroy($interface);
    }
    if (count(glob(WG_QUIC_RUN_DIR . '/quic*.pid') ?: []) === 0) {
        @rmdir(WG_QUIC_RUN_DIR);
    }
    syslog(LOG_NOTICE, "wg-quic interface {$interface} stopped");
}

function wireguardquic_carp_status()
{
    $vhids = [];
    foreach ((new OPNsense\Interfaces\Vip())->vip->iterateItems() as $id => $item) {
        if ((string)$item->mode === 'carp') {
            $vhids[$id] = ['status' => 'DISABLED', 'vhid' => (string)$item->vhid];
        }
    }
    foreach (legacy_interfaces_details() as $ifdata) {
        foreach ($ifdata['carp'] ?? [] as $data) {
            foreach ($vhids as &$item) {
                if ($item['vhid'] == $data['vhid']) {
                    $item['status'] = $data['status'];
                }
            }
            unset($item);
        }
    }
    return $vhids;
}

function wireguardquic_add_gateway($server)
{
    if ((string)$server->disableroutes !== '1' || empty((string)$server->gateway)) {
        return;
    }
    $family = str_contains((string)$server->gateway, ':') ? '-6' : '-4';
    mwexecf(
        '/sbin/route -q -n add %s %s -iface %s',
        [$family, (string)$server->gateway, (string)$server->interface]
    );
}

function wireguardquic_start_instance($server, $interfaceFlag = 'up')
{
    $interface = (string)$server->interface;
    $config = (string)$server->cnfFilename;
    if (!preg_match('/^quic[0-9]{1,3}$/', $interface)) {
        throw new RuntimeException("Invalid wg-quic interface {$interface}");
    }
    foreach ([WG_QUIC_CORE, WG_QUIC_QUICK] as $binary) {
        if (!is_file($binary) || !is_executable($binary)) {
            throw new RuntimeException("Required wg-quic binary {$binary} is missing or not executable");
        }
    }
    if (!is_file($config) || filesize($config) === 0) {
        throw new RuntimeException("Configuration for {$interface} has not been generated");
    }
    if (!chmod($config, 0600)) {
        throw new RuntimeException("Unable to secure configuration for {$interface}");
    }
    if (!is_dir(WG_QUIC_RUN_DIR) && !mkdir(WG_QUIC_RUN_DIR, 0755, true)) {
        throw new RuntimeException('Unable to create the wg-quic run directory');
    }
    $result = mwexecf(WG_QUIC_QUICK . ' check %s', [$config]);
    if ($result !== 0) {
        throw new RuntimeException("Invalid configuration for {$interface} (exit {$result})");
    }

    if (wireguardquic_pid($interface) === null) {
        if (does_interface_exist($interface)) {
            legacy_interface_destroy($interface);
        }
        @unlink(wireguardquic_pidfile($interface));
        @unlink(wireguardquic_socket($interface));
        $result = mwexecf(
            '/usr/sbin/daemon -f -S -p %s -T wg-quic %s run %s --name %s',
            [
                wireguardquic_pidfile($interface),
                WG_QUIC_QUICK,
                $config,
                $interface,
            ]
        );
        if ($result !== 0) {
            throw new RuntimeException("Unable to launch {$interface} (exit {$result})");
        }
    }

    for ($attempt = 0; $attempt < 150; $attempt++) {
        if (does_interface_exist($interface) && file_exists(wireguardquic_socket($interface))) {
            break;
        }
        usleep(100000);
    }
    if (!does_interface_exist($interface) || !file_exists(wireguardquic_socket($interface))) {
        wireguardquic_stop_interface($interface);
        throw new RuntimeException("wg-quic did not create {$interface}");
    }

    mwexecf('/sbin/ifconfig %s group wireguardquic', [$interface]);
    wireguardquic_add_gateway($server);
    interfaces_restart_by_device(false, [$interface]);
    mwexecf('/sbin/ifconfig %s %s', [$interface, $interfaceFlag]);
    syslog(LOG_NOTICE, "wg-quic instance {$server->name} ({$interface}) started");
}

function wireguardquic_servers($uuid = null)
{
    $result = [];
    foreach ((new Server())->servers->server->iterateItems() as $key => $server) {
        if ($uuid === null || $uuid === '' || $key === $uuid) {
            $result[$key] = $server;
        }
    }
    return $result;
}

function wireguardquic_stop_stale($keep = [])
{
    foreach (glob(WG_QUIC_RUN_DIR . '/quic*.pid') ?: [] as $pidfile) {
        $interface = basename($pidfile, '.pid');
        if (preg_match('/^quic[0-9]{1,3}$/', $interface) && !in_array($interface, $keep)) {
            wireguardquic_stop_interface($interface);
        }
    }
    if (count(glob(WG_QUIC_RUN_DIR . '/quic*.pid') ?: []) === 0) {
        @rmdir(WG_QUIC_RUN_DIR);
    }
}

openlog('wg-quic', LOG_ODELAY, LOG_AUTH);

$action = $argv[1] ?? '';
$uuid = $argv[2] ?? null;
$general = new General();
$servers = wireguardquic_servers($uuid);

try {
    switch ($action) {
        case 'start':
            if ((string)$general->enabled !== '1') {
                throw new RuntimeException('wg-quic is disabled');
            }
            $carp = wireguardquic_carp_status();
            foreach ($servers as $server) {
                if ((string)$server->enabled !== '1') {
                    continue;
                }
                $carpId = (string)$server->carp_depend_on;
                $flag = !empty($carp[$carpId]) && $carp[$carpId]['status'] !== 'MASTER' ? 'down' : 'up';
                wireguardquic_start_instance($server, $flag);
            }
            break;
        case 'stop':
            if (!empty($uuid)) {
                foreach ($servers as $server) {
                    wireguardquic_stop_interface((string)$server->interface);
                }
            } else {
                wireguardquic_stop_stale();
                foreach ($servers as $server) {
                    wireguardquic_stop_interface((string)$server->interface);
                }
            }
            break;
        case 'restart':
            foreach ($servers as $server) {
                wireguardquic_stop_interface((string)$server->interface);
            }
            if ((string)$general->enabled === '1') {
                $carp = wireguardquic_carp_status();
                foreach ($servers as $server) {
                    if ((string)$server->enabled !== '1') {
                        continue;
                    }
                    $carpId = (string)$server->carp_depend_on;
                    $flag = !empty($carp[$carpId]) && $carp[$carpId]['status'] !== 'MASTER' ? 'down' : 'up';
                    wireguardquic_start_instance($server, $flag);
                }
            }
            break;
        case 'configure':
            $enabledInterfaces = [];
            if ((string)$general->enabled === '1') {
                foreach ($servers as $server) {
                    if ((string)$server->enabled === '1') {
                        $enabledInterfaces[] = (string)$server->interface;
                    }
                }
            }
            wireguardquic_stop_stale($enabledInterfaces);
            foreach ($servers as $server) {
                wireguardquic_stop_interface((string)$server->interface);
            }
            if ((string)$general->enabled === '1') {
                $carp = wireguardquic_carp_status();
                foreach ($servers as $server) {
                    if ((string)$server->enabled !== '1') {
                        continue;
                    }
                    $carpId = (string)$server->carp_depend_on;
                    $flag = !empty($carp[$carpId]) && $carp[$carpId]['status'] !== 'MASTER' ? 'down' : 'up';
                    wireguardquic_start_instance($server, $flag);
                }
            }
            break;
        case 'status':
            $running = [];
            foreach (wireguardquic_servers($uuid) as $server) {
                $pid = wireguardquic_pid((string)$server->interface);
                if ($pid !== null) {
                    $running[] = "{$server->interface} (pid {$pid})";
                }
            }
            if (count($running) === 0) {
                echo "wg-quic is not running.\n";
                exit(1);
            }
            echo 'wg-quic is running: ' . implode(', ', $running) . ".\n";
            break;
        default:
            fwrite(STDERR, "Usage: service-control.php start|stop|restart|configure|status [uuid]\n");
            exit(64);
    }
} catch (Throwable $error) {
    syslog(LOG_ERR, $error->getMessage());
    fwrite(STDERR, $error->getMessage() . "\n");
    exit(1);
}
