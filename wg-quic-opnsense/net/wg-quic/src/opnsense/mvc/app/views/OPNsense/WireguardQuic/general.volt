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
        toggle: '/api/wireguardquic/server/toggle_server/'
    });

    $('#reconfigureAct').SimpleActionButton({
        onPreAction: function() {
            const deferred = new $.Deferred();
            saveFormToEndpoint('/api/wireguardquic/general/set', 'frm_general_settings', function() {
                deferred.resolve();
            }, true, function() {
                deferred.reject();
            });
            return deferred;
        }
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
