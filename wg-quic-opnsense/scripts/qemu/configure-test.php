#!/usr/local/bin/php
<?php

/*
 * Configure a deterministic two-daemon wg-quic smoke test.
 * This file is intended to run only inside a disposable OPNsense VM.
 */

require_once('script/load_phalcon.php');

use OPNsense\WireguardQuic\General;
use OPNsense\WireguardQuic\Client;
use OPNsense\WireguardQuic\Server;
use OPNsense\Core\Config;

function wg_command(array $arguments, $input = null)
{
    $descriptorSpec = [
        0 => ['pipe', 'r'],
        1 => ['pipe', 'w'],
        2 => ['pipe', 'w'],
    ];
    $process = proc_open($arguments, $descriptorSpec, $pipes);
    if (!is_resource($process)) {
        throw new RuntimeException('Unable to execute wg-quic');
    }
    if ($input !== null) {
        fwrite($pipes[0], $input);
    }
    fclose($pipes[0]);
    $stdout = stream_get_contents($pipes[1]);
    $stderr = stream_get_contents($pipes[2]);
    fclose($pipes[1]);
    fclose($pipes[2]);
    $status = proc_close($process);
    if ($status !== 0) {
        throw new RuntimeException('wg-quic failed: ' . trim($stderr));
    }
    return trim($stdout);
}

$stateFile = '/tmp/wg-quic-test-state.json';
if (($argv[1] ?? '') === 'server-stage') {
    $state = json_decode(file_get_contents($stateFile), true, 512, JSON_THROW_ON_ERROR);
    $servers = new Server();
    foreach ($servers->servers->server->iterateItems() as $uuid => $unused) {
        $servers->servers->server->del($uuid);
    }
    $server = $servers->servers->server->Add();
    $server->enabled->setValue('1');
    $server->name->setValue('qemu-instance');
    $server->instance->setValue('0');
    $server->pubkey->setValue($state['public0']);
    $server->privkey->setValue($state['private0']);
    $server->port->setValue('52820');
    $server->mtu->setValue('1420');
    $server->congestion->setValue('auto');
    $server->fec->setValue('auto');
    $server->obfs->setValue('salamander');
    $server->tunneladdress->setValue('10.66.0.1/24');
    $server->disableroutes->setValue('1');
    $server->peers->setValue($state['peerUuid']);

    $validation = $servers->performValidation();
    if (count($validation) !== 0) {
        foreach ($validation as $field => $message) {
            fwrite(STDERR, "{$field}: {$message}\n");
        }
        exit(1);
    }
    $servers->serializeToConfig();
    Config::getInstance()->save();

    $peerConfig = <<<CONFIG
[Interface]
PrivateKey = {$state['private1']}
Address = 10.66.0.2/32
ListenPort = 52821
MTU = 1420
Table = off
# wg-quic: congestion = auto
# wg-quic: fec = auto
# wg-quic: obfs = salamander

[Peer]
PublicKey = {$state['public0']}
Endpoint = 127.0.0.1:52820
AllowedIPs = 10.66.0.1/32
PersistentKeepalive = 1
CONFIG;

    $configDir = '/usr/local/etc/wg-quic';
    if (!is_dir($configDir) && !mkdir($configDir, 0700, true)) {
        throw new RuntimeException('Unable to create wg-quic configuration directory');
    }
    chmod($configDir, 0700);
    $peerConfigPath = $configDir . '/quic1.conf';
    file_put_contents($peerConfigPath, $peerConfig . "\n");
    chmod($peerConfigPath, 0600);
    echo "test model configured\n";
    exit(0);
}

$private0 = wg_command(['/usr/local/sbin/wg-quic', 'genkey']);
$public0 = wg_command(['/usr/local/sbin/wg-quic', 'pubkey'], $private0 . "\n");
$private1 = wg_command(['/usr/local/sbin/wg-quic', 'genkey']);
$public1 = wg_command(['/usr/local/sbin/wg-quic', 'pubkey'], $private1 . "\n");

$general = new General();
$general->enabled->setValue('1');
$clients = new Client();

foreach ($clients->clients->client->iterateItems() as $uuid => $unused) {
    $clients->clients->client->del($uuid);
}

$peer = $clients->clients->client->Add();
$peer->enabled->setValue('1');
$peer->name->setValue('qemu-peer');
$peer->pubkey->setValue($public1);
$peer->serveraddress->setValue('127.0.0.1');
$peer->serverport->setValue('52821');
$peer->tunneladdress->setValue('10.66.0.2/32');
$peer->keepalive->setValue('1');
$peer->fec_policy->setValue('balanced');
$peerUuid = $peer->getAttribute('uuid');
if (empty($peerUuid)) {
    throw new RuntimeException('Unable to determine peer UUID');
}

foreach ([$general, $clients] as $model) {
    $validation = $model->performValidation();
    if (count($validation) !== 0) {
        foreach ($validation as $field => $message) {
            fwrite(STDERR, "{$field}: {$message}\n");
        }
        exit(1);
    }
    $model->serializeToConfig();
}
Config::getInstance()->save();

$state = compact('private0', 'public0', 'private1', 'peerUuid');
file_put_contents($stateFile, json_encode($state, JSON_THROW_ON_ERROR));
chmod($stateFile, 0600);
echo wg_command([PHP_BINARY, __FILE__, 'server-stage']) . "\n";
unlink($stateFile);
