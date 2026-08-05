#!/usr/local/bin/php
<?php

/*
 * Create or remove a temporary root API key inside a disposable OPNsense VM.
 * The plaintext credential file never leaves the guest.
 */

require_once('script/load_phalcon.php');

use OPNsense\Auth\User;
use OPNsense\Core\Config;

const CREDENTIAL_FILE = '/tmp/wg-quic-api-credentials';

$action = $argv[1] ?? '';
$model = new User();
$root = $model->getUserByName('root');
if ($root === null) {
    fwrite(STDERR, "root user was not found\n");
    exit(1);
}

if ($action === 'create') {
    $credentials = $root->apikeys->add();
    $model->serializeToConfig(false, true);
    Config::getInstance()->save();
    file_put_contents(
        CREDENTIAL_FILE,
        $credentials['key'] . "\n" . $credentials['secret'] . "\n"
    );
    chmod(CREDENTIAL_FILE, 0600);
    echo "temporary API credentials created\n";
} elseif ($action === 'remove') {
    if (is_file(CREDENTIAL_FILE)) {
        $lines = file(CREDENTIAL_FILE, FILE_IGNORE_NEW_LINES);
        if (!empty($lines[0])) {
            $root->apikeys->del($lines[0]);
            $model->serializeToConfig(false, true);
            Config::getInstance()->save();
        }
        unlink(CREDENTIAL_FILE);
    }
    echo "temporary API credentials removed\n";
} else {
    fwrite(STDERR, "usage: api-credentials.php create|remove\n");
    exit(64);
}
