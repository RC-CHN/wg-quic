#!/usr/local/bin/php
<?php

/*
 * Configure the OPNsense side of the Linux netstack interoperability test.
 * This file is intended to run only inside a disposable OPNsense VM.
 */

require_once('script/load_phalcon.php');

use OPNsense\Core\Config;
use OPNsense\WireguardQuic\Client;
use OPNsense\WireguardQuic\General;
use OPNsense\WireguardQuic\Server;

function validate_and_save($model)
{
    $validation = $model->performValidation();
    if (count($validation) !== 0) {
        foreach ($validation as $field => $message) {
            fwrite(STDERR, "{$field}: {$message}\n");
        }
        exit(1);
    }
    $model->serializeToConfig();
    Config::getInstance()->save();
}

$serverStage = ($argv[1] ?? '') === 'server-stage';
$path = $serverStage ? ($argv[2] ?? '') : ($argv[1] ?? '');
$payload = is_file($path) ? json_decode(file_get_contents($path), true) : null;
foreach (
    [
        'guestPrivateKey',
        'guestPublicKey',
        'clientPublicKey',
        'guestAddress',
        'clientAddress',
    ] as $required
) {
    if (!is_array($payload) || empty($payload[$required])) {
        fwrite(STDERR, "missing interoperability setting: {$required}\n");
        exit(1);
    }
}

if ($serverStage) {
    $peerUuid = $argv[3] ?? '';
    if ($peerUuid === '') {
        fwrite(STDERR, "missing peer UUID\n");
        exit(1);
    }
    $servers = new Server();
    foreach ($servers->servers->server->iterateItems() as $uuid => $unused) {
        $servers->servers->server->del($uuid);
    }
    $server = $servers->servers->server->Add();
    $server->enabled->setValue('1');
    $server->name->setValue('linux-interoperability');
    $server->instance->setValue('0');
    $server->pubkey->setValue($payload['guestPublicKey']);
    $server->privkey->setValue($payload['guestPrivateKey']);
    $server->port->setValue('52820');
    $server->tunneladdress->setValue($payload['guestAddress'] . '/32');
    $server->mtu->setValue('1420');
    $server->disableroutes->setValue('0');
    $server->peers->setValue($peerUuid);
    $server->congestion->setValue('auto');
    $server->fec->setValue('auto');
    $server->obfs->setValue('salamander');
    validate_and_save($servers);
    echo "OPNsense interoperability endpoint configured\n";
    exit(0);
}

$general = new General();
$general->enabled->setValue('1');
$clients = new Client();

foreach ($clients->clients->client->iterateItems() as $uuid => $unused) {
    $clients->clients->client->del($uuid);
}

$peer = $clients->clients->client->Add();
$peer->enabled->setValue('1');
$peer->name->setValue('linux-reference-client');
$peer->pubkey->setValue($payload['clientPublicKey']);
$peer->tunneladdress->setValue($payload['clientAddress'] . '/32');
$peer->fec_policy->setValue('balanced');
$peerUuid = $peer->getAttribute('uuid');
if (empty($peerUuid)) {
    throw new RuntimeException('Unable to determine peer UUID');
}

validate_and_save($general);
validate_and_save($clients);

$command = implode(' ', array_map('escapeshellarg', [
    PHP_BINARY,
    __FILE__,
    'server-stage',
    $path,
    $peerUuid,
]));
passthru($command, $status);
exit($status);
