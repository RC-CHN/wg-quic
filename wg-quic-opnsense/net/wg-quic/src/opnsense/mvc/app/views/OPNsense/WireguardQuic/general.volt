{#
 # Copyright (c) 2014-2023 Deciso B.V.
 # Copyright (c) 2018 Michael Muenz <m.muenz@gmail.com>
 # Copyright (c) 2026 wg-quic contributors
 # SPDX-License-Identifier: BSD-2-Clause
 #}

<script>
$(document).ready(function() {
    mapDataToFormUI({'frm_general_settings': '/api/wireguardquic/general/get'}).done(function() {
        formatTokenizersUI();
        $('.selectpicker').selectpicker('refresh');
    });

    const clientGrid = $('#{{clientGrid["table_id"]}}').UIBootgrid({
        search: '/api/wireguardquic/client/search_client',
        get: '/api/wireguardquic/client/get_client/',
        set: '/api/wireguardquic/client/set_client/',
        add: '/api/wireguardquic/client/add_client/',
        del: '/api/wireguardquic/client/del_client/',
        toggle: '/api/wireguardquic/client/toggle_client/',
        options: {
            initialSearchPhrase: getUrlHash('search'),
            requestHandler: function(request) {
                if ($('#server_filter').val().length > 0) {
                    request.servers = $('#server_filter').val();
                }
                return request;
            }
        }
    });
    clientGrid.on('loaded.rs.jquery.bootgrid', function() {
        if ($('#server_filter > option').length === 0) {
            ajaxGet('/api/wireguardquic/client/list_servers', {}, function(data) {
                if (data.rows !== undefined) {
                    data.rows.forEach(function(row) {
                        $('#server_filter').append($('<option/>').val(row.uuid).html(row.name));
                    });
                    $('#server_filter').selectpicker('refresh');
                }
            });
        }
    });

    $('#{{serverGrid["table_id"]}}').UIBootgrid({
        search: '/api/wireguardquic/server/search_server',
        get: '/api/wireguardquic/server/get_server/',
        set: '/api/wireguardquic/server/set_server/',
        add: '/api/wireguardquic/server/add_server/',
        del: '/api/wireguardquic/server/del_server/',
        toggle: '/api/wireguardquic/server/toggle_server/',
        options: {triggerEditFor: /^[0-9a-f-]{36}$/i.test(getUrlHash('edit') || '') ? getUrlHash('edit') : null}
    });

    WgQuicSettings.peerTransport('{{clientGrid["edit_dialog_id"]}}', {
        title: {{ lang._('Transport settings — inherited from instances') | json_encode() }},
        scope: {{ lang._('Congestion control, FEC and obfuscation are read-only here. Edit the owning instance to change them for all of its peers.') | json_encode() }},
        unassigned: {{ lang._('This peer is not assigned to an instance. No instance transport settings apply yet.') | json_encode() }},
        unassignedPolicy: {{ lang._('This preference is saved with the peer and takes effect when it is assigned to an instance with FEC enabled.') | json_encode() }},
        unavailable: {{ lang._('Instance settings could not be loaded. Their values are unknown; defaults are not assumed.') | json_encode() }},
        unknownPolicy: {{ lang._('Instance FEC state is unknown. The peer preference remains editable.') | json_encode() }},
        instance: {{ lang._('Instance') | json_encode() }},
        congestion: {{ lang._('Congestion control') | json_encode() }},
        fec: {{ lang._('FEC') | json_encode() }},
        obfs: {{ lang._('Obfuscation') | json_encode() }},
        edit: {{ lang._('Edit') | json_encode() }},
        disabled: {{ lang._('disabled') | json_encode() }},
        editNewTab: {{ lang._('Edit instance (new tab)') | json_encode() }},
        savedSettings: {{ lang._('These are saved settings, not proof of what is currently running. Apply changes on the configuration page; transport changes require restarting the affected instance.') | json_encode() }},
        fecOff: {{ lang._('FEC is disabled on every selected instance. This preference is kept but currently inactive.') | json_encode() }},
        fecMixed: {{ lang._('This preference only applies to selected instances with FEC enabled.') | json_encode() }},
        fecActive: {{ lang._('This peer-level preference applies where instance FEC is enabled.') | json_encode() }},
        loading: {{ lang._('Loading instance settings…') | json_encode() }},
        retry: {{ lang._('Retry') | json_encode() }}
    });

    $(document.getElementById('server.congestion')).closest('tr').before(
        $('<tr/>').append($('<td colspan="3"/>').append(
            $('<div class="alert alert-info" id="instance-transport-scope"/>').text(
                {{ lang._('These transport settings apply to every peer on this instance. Saving does not restart connections. Applying a transport change requires a separate restart confirmation and interrupts all peers on this instance; remote profiles are not updated automatically.') | json_encode() }}
            )
        ))
    );

    WgQuicSettings.applyWorkflow({
        apply: {{ lang._('Apply saved changes') | json_encode() }},
        applying: {{ lang._('Applying…') | json_encode() }},
        applied: {{ lang._('Saved settings applied.') | json_encode() }},
        restart_required: {{ lang._('Settings are saved but are not active yet. This instance needs a restart.') | json_encode() }},
        failed: {{ lang._('Application failed. Review the error before trying again.') | json_encode() }},
        unknown: {{ lang._('The result could not be confirmed. Check the instance status or query the original operation; do not assume the change failed.') | json_encode() }},
        cleanup: {{ lang._('The change was applied but resource cleanup is still pending.') | json_encode() }},
        restart: {{ lang._('Restart instance') | json_encode() }},
        restartWarning: {{ lang._('Restart {instance}? This interrupts every connection on this instance ({count} configured peers). Other instances will not be restarted. Remote profiles must be updated separately when obfuscation changes.') | json_encode() }},
        cancel: {{ lang._('Cancel') | json_encode() }},
        query: {{ lang._('Check operation result') | json_encode() }},
        saveFailed: {{ lang._('Settings could not be saved. Nothing further was applied.') | json_encode() }}
    });

    $('#control_label_server\\.pubkey').append($('#keygen_div').detach().show());
    $('#keygen').click(function() {
        ajaxGet('/api/wireguardquic/server/key_pair', {}, function(data) {
            if (data.status === 'ok') {
                $('#server\\.pubkey').val(data.pubkey);
                $('#server\\.privkey').val(data.privkey);
            }
        });
    });

    $('#control_label_client\\.psk').append($('#pskgen_div').detach().show());
    $('#pskgen').click(function() {
        ajaxGet('/api/wireguardquic/client/psk', {}, function(data) {
            if (data.status === 'ok') {
                $('#client\\.psk').val(data.psk);
            }
        });
    });

    $('#filter_container').detach().insertAfter('#{{clientGrid["table_id"]}}-header .search');
    $('#server_filter').change(function() {
        $('#{{clientGrid["table_id"]}}').bootgrid('reload');
    });

    $('#control_label_configbuilder\\.psk').append($('#pskgen_cb_div').detach().show());
    $('#pskgen_cb').click(function() {
        ajaxGet('/api/wireguardquic/client/psk', {}, function(data) {
            if (data.status === 'ok') {
                $('#configbuilder\\.psk').val(data.psk).change();
            }
        });
    });

    const outputRow = $('#configbuilder\\.output').closest('tr');
    outputRow.find('td:eq(2)').empty().append($('<div id="qrcode"/>'));
    $('#configbuilder\\.output').css('max-width', '100%').css('height', '256px').change(function() {
        $('#qrcode').empty().qrcode($(this).val());
    });

    $('#configbuilder\\.servers').change(function() {
        ajaxGet('/api/wireguardquic/client/get_server_info/' + $(this).val(), {}, function(data) {
            if (data.status === 'ok') {
                const endpoint = $('#configbuilder\\.endpoint');
                const peerDns = $('#configbuilder\\.peer_dns');
                $('#configbuilder\\.address').val(data.address);
                peerDns.val(data.peer_dns).data('org-value', data.peer_dns);
                endpoint
                    .val(data.endpoint)
                    .data('org-value', data.endpoint)
                    .data('mtu', data.mtu)
                    .data('pubkey', data.pubkey)
                    .data('congestion', data.congestion || 'auto')
                    .data('fec', data.fec || 'auto')
                    .data('obfs', data.obfs || 'salamander')
                    .change();
            }
        });
    });

    $('#configbuilder\\.store_btn').replaceWith($('#btn_configbuilder_save'));
    $('#btn_configbuilder_save').click(function() {
        const instanceId = $('#configbuilder\\.servers').val();
        const endpoint = $('#configbuilder\\.endpoint');
        const peerDns = $('#configbuilder\\.peer_dns');
        const peer = {
            configbuilder: {
                enabled: '1',
                name: $('#configbuilder\\.name').val(),
                pubkey: $('#configbuilder\\.pubkey').val(),
                psk: $('#configbuilder\\.psk').val(),
                tunneladdress: $('#configbuilder\\.address').val(),
                keepalive: $('#configbuilder\\.keepalive').val(),
                server: instanceId,
                endpoint: endpoint.val()
            }
        };
        ajaxCall('/api/wireguardquic/client/add_client_builder', peer, function(data) {
            if (data.validations) {
                if (data.validations['configbuilder.tunneladdress']) {
                    data.validations['configbuilder.address'] =
                        data.validations['configbuilder.tunneladdress'];
                    delete data.validations['configbuilder.tunneladdress'];
                }
                handleFormValidation('frm_config_builder', data.validations);
            } else if (
                endpoint.val() !== endpoint.data('org-value') ||
                peerDns.val() !== peerDns.data('org-value')
            ) {
                ajaxCall(
                    '/api/wireguardquic/server/set_server/' + instanceId,
                    {server: {endpoint: endpoint.val(), peer_dns: peerDns.val()}},
                    configBuilderNew
                );
            } else {
                configBuilderNew();
            }
        });
    });

    $('input[id ^= "configbuilder\\."]').change(configBuilderUpdate);
    $('select[id ^= "configbuilder\\."]').change(configBuilderUpdate);

    function configBuilderNew() {
        mapDataToFormUI({
            'frm_config_builder': '/api/wireguardquic/client/get_client_builder'
        }).done(function() {
            formatTokenizersUI();
            $('.selectpicker').selectpicker('refresh');
            ajaxGet('/api/wireguardquic/server/key_pair', {}, function(data) {
                if (data.status === 'ok') {
                    $('#configbuilder\\.pubkey').val(data.pubkey);
                    $('#configbuilder\\.privkey').val(data.privkey).change();
                }
            });
            $('#configbuilder\\.tunneladdress').val('0.0.0.0/0,::/0');
            clearFormValidation('frm_config_builder');
        });
    }

    function configBuilderUpdate() {
        const rows = ['[Interface]'];
        rows.push('PrivateKey = ' + $('#configbuilder\\.privkey').val());
        if ($('#configbuilder\\.address').val()) {
            rows.push('Address = ' + $('#configbuilder\\.address').val());
        }
        if ($('#configbuilder\\.peer_dns').val()) {
            rows.push('DNS = ' + $('#configbuilder\\.peer_dns').val());
        }
        if ($('#configbuilder\\.endpoint').data('mtu')) {
            rows.push('MTU = ' + $('#configbuilder\\.endpoint').data('mtu'));
        }
        rows.push(
            '# wg-quic: congestion = ' +
            ($('#configbuilder\\.endpoint').data('congestion') || 'auto')
        );
        rows.push(
            '# wg-quic: fec = ' +
            ($('#configbuilder\\.endpoint').data('fec') || 'auto')
        );
        rows.push(
            '# wg-quic: obfs = ' +
            ($('#configbuilder\\.endpoint').data('obfs') || 'salamander')
        );
        rows.push('', '[Peer]');
        rows.push('# wg-quic: peer.fec-latency = balanced');
        rows.push('PublicKey = ' + $('#configbuilder\\.endpoint').data('pubkey'));
        if ($('#configbuilder\\.psk').val()) {
            rows.push('PresharedKey = ' + $('#configbuilder\\.psk').val());
        }
        rows.push('Endpoint = ' + $('#configbuilder\\.endpoint').val());
        rows.push('AllowedIPs = ' + $('#configbuilder\\.tunneladdress').val());
        if ($('#configbuilder\\.keepalive').val()) {
            rows.push('PersistentKeepalive = ' + $('#configbuilder\\.keepalive').val());
        }
        $('#configbuilder\\.output').val(rows.join('\n')).change();
    }

    $('a[data-toggle="tab"]').on('shown.bs.tab', function(event) {
        if (event.target.id === 'tab_configbuilder') {
            configBuilderNew();
        } else if (event.target.id === 'tab_peers') {
            $('#{{clientGrid["table_id"]}}').bootgrid('reload');
        } else if (event.target.id === 'tab_instances') {
            $('#{{serverGrid["table_id"]}}').bootgrid('reload');
        }
    });

    if (window.location.hash !== '') {
        $('a[href="' + window.location.hash.split('&')[0] + '"]').click();
    }
    $('.nav-tabs a').on('shown.bs.tab', function(event) {
        history.pushState(null, null, event.target.hash);
    });
    $(window).on('hashchange', function() {
        $('a[href="' + window.location.hash.split('&')[0] + '"]').click();
    });
});
</script>

<ul class="nav nav-tabs" data-tabs="tabs" id="maintabs">
    <li class="active">
        <a data-toggle="tab" id="tab_instances" href="#instances">{{ lang._('Instances') }}</a>
    </li>
    <li>
        <a data-toggle="tab" id="tab_peers" href="#peers">{{ lang._('Peers') }}</a>
    </li>
    <li>
        <a data-toggle="tab" id="tab_configbuilder" href="#configbuilder">{{ lang._('Peer generator') }}</a>
    </li>
</ul>

<div class="tab-content content-box tab-content">
    <div id="peers" class="tab-pane fade in">
        <span id="pskgen_div" style="display:none" class="pull-right">
            <button id="pskgen" type="button" class="btn btn-secondary"
                    title="{{ lang._('Generate new psk.') }}" data-toggle="tooltip">
                <i class="fa fa-fw fa-gear"></i>
            </button>
        </span>
        <div class="hidden">
            <div id="filter_container" class="btn-group">
                <select id="server_filter" data-title="{{ lang._('Instances') }}"
                        class="selectpicker" data-live-search="true" data-size="5"
                        multiple data-width="200px">
                </select>
            </div>
        </div>
        {{ partial('layout_partials/base_bootgrid_table', clientGrid) }}
    </div>
    <div id="instances" class="tab-pane fade in active">
        <span id="keygen_div" style="display:none" class="pull-right">
            <button id="keygen" type="button" class="btn btn-secondary"
                    title="{{ lang._('Generate new keypair.') }}" data-toggle="tooltip">
                <i class="fa fa-fw fa-gear"></i>
            </button>
        </span>
        {{ partial('layout_partials/base_bootgrid_table', serverGrid) }}
    </div>
    <div id="configbuilder" class="tab-pane fade in">
        <span id="pskgen_cb_div" style="display:none" class="pull-right">
            <button id="pskgen_cb" type="button" class="btn btn-secondary"
                    title="{{ lang._('Generate new psk.') }}" data-toggle="tooltip">
                <i class="fa fa-fw fa-gear"></i>
            </button>
        </span>
        <span id="configbuilder_div" style="display:none">
            <button id="btn_configbuilder_save" type="button" class="btn btn-primary">
                <i class="fa fa-fw fa-check"></i>
            </button>
        </span>
        {{ partial("layout_partials/base_form", ['fields':configBuilderForm, 'id':'frm_config_builder']) }}
    </div>
    {{ partial("layout_partials/base_form", ['fields':generalForm, 'id':'frm_general_settings']) }}
</div>

{{ partial('layout_partials/base_apply_button', {'data_endpoint':'/api/wireguardquic/service/reconfigure'}) }}
{{ partial("layout_partials/base_dialog", [
    'fields':clientForm,
    'id':clientGrid['edit_dialog_id'],
    'label':lang._('Edit peer')
]) }}
{{ partial("layout_partials/base_dialog", [
    'fields':serverForm,
    'id':serverGrid['edit_dialog_id'],
    'label':lang._('Edit instance')
]) }}
