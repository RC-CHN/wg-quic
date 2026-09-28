<?php
/* SPDX-License-Identifier: GPL-3.0-or-later */
/* Included by service-control.php; only fixed configd actions call these helpers. */

function wireguardquic_apply_result($interface, $requestId = '')
{
    $args = [$interface];
    $command = WG_QUIC_QUICK . ' desktop-apply %s';
    if ($requestId !== '') {
        if (!preg_match('/^[0-9a-f]{32}$/', $requestId)) {
            throw new RuntimeException('Invalid apply request ID');
        }
        $command .= ' %s';
        $args[] = $requestId;
    }
    $result = json_decode(shell_safe($command, $args), true);
    if (!is_array($result) || !in_array($result['state'] ?? '', ['applied', 'restart_required', 'failed', 'unknown'])) {
        return ['state' => 'unknown', 'code' => 'invalid_response', 'message' => 'Could not confirm the apply result.'];
    }
    return $result;
}

function wireguardquic_restart_token($server)
{
    $status = json_decode(shell_safe(WG_QUIC_QUICK . ' show %s --json', [(string)$server->interface]), true);
    $digest = @hash_file('sha256', (string)$server->cnfFilename);
    if (!$digest || empty($status['supervisor_epoch']) || !isset($status['desired_generation'])) {
        throw new RuntimeException('Could not verify the running instance before restart. Apply again to refresh its state.');
    }
    return hash('sha256', $digest . ':' . $status['supervisor_epoch'] . ':' . $status['desired_generation']);
}

function wireguardquic_web_apply($servers, $general, $action = 'apply', $argument = '')
{
    $reports = [];
    if ($action !== 'apply' && count($servers) !== 1) {
        throw new RuntimeException('Select exactly one existing instance.');
    }
    if ($action === 'apply') {
        $keep = [];
        if ((string)$general->enabled === '1') {
            foreach ($servers as $server) {
                if ((string)$server->enabled === '1') {
                    $keep[] = (string)$server->interface;
                }
            }
        }
        wireguardquic_stop_stale($keep);
    }
    $carp = wireguardquic_carp_status();
    foreach ($servers as $uuid => $server) {
        $interface = (string)$server->interface;
        $report = ['uuid' => $uuid, 'name' => (string)$server->name, 'interface' => $interface,
            'peer_count' => count(array_filter(explode(',', (string)$server->peers)))];
        try {
            $enabled = (string)$general->enabled === '1' && (string)$server->enabled === '1';
            $carpId = (string)$server->carp_depend_on;
            $flag = !empty($carpId) && ($carp[$carpId]['status'] ?? '') !== 'MASTER' ? 'down' : 'up';
            if ($action === 'query') {
                $result = wireguardquic_apply_result($interface, $argument);
            } elseif ($action === 'restart') {
                if (!$enabled || !preg_match('/^[0-9a-f]{64}$/', $argument) ||
                    !hash_equals(wireguardquic_restart_token($server), $argument)) {
                    throw new RuntimeException('Configuration or runtime changed since confirmation. Apply again before restarting.');
                }
                wireguardquic_stop_interface($interface);
                wireguardquic_start_instance($server, $flag);
                $result = ['state' => 'applied', 'code' => 'restarted'];
            } elseif (!$enabled) {
                wireguardquic_stop_interface($interface);
                $result = ['state' => 'applied', 'code' => 'stopped'];
            } elseif (wireguardquic_pid($interface) === null) {
                wireguardquic_start_instance($server, $flag);
                $result = ['state' => 'applied', 'code' => 'started'];
            } else {
                $result = wireguardquic_apply_result($interface);
                if ($result['state'] === 'applied') {
                    mwexecf('/sbin/ifconfig %s %s', [$interface, $flag]);
                }
            }
            if ($result['state'] === 'restart_required') {
                $result['restart_token'] = wireguardquic_restart_token($server);
            }
            $report = array_merge($report, $result);
        } catch (Throwable $error) {
            $report['state'] = 'failed';
            $report['message'] = $error->getMessage();
        }
        $reports[] = $report;
    }
    $ok = count(array_filter($reports, fn($row) => $row['state'] !== 'applied' || !empty($row['cleanup_pending']))) === 0;
    return ['status' => $ok ? 'ok' : 'attention', 'result' => $ok ? 'ok' : 'failed', 'instances' => $reports];
}
