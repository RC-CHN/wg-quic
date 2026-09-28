/* SPDX-License-Identifier: GPL-3.0-or-later */
/* Instance settings are projections, never editable peer fields. */
window.WgQuicSettings = (() => {
    'use strict';
    function peerTransport(modalId, text) {
        const modal = $(document.getElementById(modalId));
        const membership = $(document.getElementById('client.servers'));
        const policy = $(document.getElementById('client.fec_policy'));
        const panel = $('<section class="alert alert-info" id="peer-transport-settings" aria-live="polite"/>');
        membership.closest('tr').after($('<tr/>').append($('<td colspan="3"/>').append(panel)));
        const policyNote = $('<p id="peer-fec-note" class="help-block"/>');
        policy.closest('td').append(policyNote);
        let serial = 0;
        function setPolicyEnabled(enabled) {
            policy.prop('disabled', !enabled).selectpicker('refresh');
        }
        function render(rows) {
            panel.empty().append($('<strong/>').text(text.title), $('<p/>').text(text.scope));
            const ids = membership.val() || [];
            if (!ids.length) {
                panel.append($('<p/>').text(text.unassigned));
                policyNote.text(text.unassignedPolicy);
                setPolicyEnabled(true);
                return;
            }
            const selected = ids.map((id) => rows.find((row) => row.uuid === id));
            if (selected.some((row) => !row)) {
                panel.append($('<p/>').text(text.unavailable));
                policyNote.text(text.unknownPolicy);
                setPolicyEnabled(true);
                return;
            }
            const table = $('<table class="table table-condensed"/>');
            const head = $('<tr/>');
            [text.instance, text.congestion, text.fec, text.obfs, text.edit].forEach((label) => head.append($('<th/>').text(label)));
            table.append($('<thead/>').append(head));
            const body = $('<tbody/>');
            selected.forEach((row) => {
                const tr = $('<tr/>');
                tr.append($('<td/>').text(row.name + ' (' + row.interface + ')' + (row.enabled ? '' : ' — ' + text.disabled)));
                [row.congestion, row.fec, row.obfs].forEach((value) => tr.append($('<td/>').text(value)));
                const link = $('<a target="_blank" rel="noopener"/>')
                    .attr('href', '/ui/wireguardquic/general#instances&edit=' + encodeURIComponent(row.uuid))
                    .text(text.editNewTab);
                tr.append($('<td/>').append(link));
                body.append(tr);
            });
            table.append(body);
            panel.append(table, $('<p/>').text(text.savedSettings));
            const enabled = selected.filter((row) => row.fec !== 'off');
            setPolicyEnabled(enabled.length > 0);
            policyNote.text(enabled.length === 0 ? text.fecOff : enabled.length < selected.length ? text.fecMixed : text.fecActive);
        }
        function refresh() {
            const request = ++serial;
            panel.empty().append($('<p/>').text(text.loading));
            $.ajax({url: '/api/wireguardquic/client/list_servers', dataType: 'json', timeout: 10000})
                .done((data) => {
                    if (request !== serial) return;
                    if (data.status === 'ok' && Array.isArray(data.rows)) render(data.rows);
                    else failed();
                }).fail(() => { if (request === serial) failed(); });
        }
        function failed() {
            panel.empty().append($('<p/>').text(text.unavailable), $('<button type="button" class="btn btn-default"/>').text(text.retry).on('click', refresh));
            policyNote.text(text.unknownPolicy);
            setPolicyEnabled(true);
        }
        membership.on('change', refresh);
        modal.on('shown.bs.modal opnsense_bootgrid_mapped', refresh);
        modal.on('hidden.bs.modal', () => { serial++; });
        $(window).on('focus', () => { if (modal.hasClass('in')) refresh(); });
    }
    function applyWorkflow(text) {
        const button = $('#reconfigureAct').text(text.apply);
        const panel = $('<div id="wg-quic-apply-results" aria-live="polite"/>').insertAfter(button.closest('section'));
        let busy = false;
        let revision = 0;
        let reports = new Map();
        function setBusy(value) {
            busy = value;
            button.prop('disabled', value).text(value ? text.applying : text.apply);
            panel.find('button').prop('disabled', value);
        }
        function render(data, replace) {
            if (replace) reports = new Map();
            (data.instances || []).forEach((row) => reports.set(row.uuid, row));
            panel.empty();
            if (data.message) panel.append($('<div class="alert alert-warning"/>').text(data.message));
            if (!reports.size && data.status !== 'ok' && !data.message) {
                panel.append($('<div class="alert alert-warning"/>').text(text.unknown));
            }
            reports.forEach((row) => {
                const ok = row.state === 'applied' && !row.cleanup_pending;
                const card = $('<div class="alert"/>').addClass(ok ? 'alert-success' : 'alert-warning');
                card.append($('<strong/>').text(row.name + ' (' + row.interface + ')'),
                    $('<p/>').text(text[row.state] || text.unknown));
                if (row.cleanup_pending) card.append($('<p/>').text(text.cleanup));
                if (row.message) card.append($('<p/>').text(row.message));
                if (row.restart_reasons?.length) card.append($('<p/>').text(row.restart_reasons.join('; ')));
                if (row.state === 'restart_required' && row.restart_token) {
                    card.append($('<button type="button" class="btn btn-warning"/>').text(text.restart + ' ' + row.interface).on('click', () => {
                        if (busy) return;
                        stdDialogConfirm(text.restart, text.restartWarning.replace('{instance}', row.name + ' (' + row.interface + ')').replace('{count}', row.peer_count), text.restart, text.cancel, () => {
                            run('/api/wireguardquic/service/restart_instance/' + encodeURIComponent(row.uuid), {restart_token: row.restart_token}, false);
                        });
                    }));
                } else if (row.state === 'unknown' && row.request_id) {
                    card.append($('<button type="button" class="btn btn-default"/>').text(text.query).on('click', () => {
                        run('/api/wireguardquic/service/query_apply/' + encodeURIComponent(row.uuid), {request_id: row.request_id}, false);
                    }));
                }
                panel.append(card);
            });
            const pending = [...reports.values()].some((row) => row.state !== 'applied' || row.cleanup_pending);
            $('#change_message_base_form').toggle(pending || data.status !== 'ok');
        }
        function run(url, payload, replace) {
            if (busy) return;
            setBusy(true);
            const submittedRevision = revision;
            $.ajax({url, type: 'POST', data: payload, dataType: 'json', timeout: 130000})
                .done((data) => {
                    if (submittedRevision !== revision) return;
                    if (typeof data?.status !== 'string' || !Array.isArray(data.instances)) {
                        render({status:'failed', instances:[], message:text.unknown}, true);
                    } else render(data, replace);
                }).fail(() => {
                    if (submittedRevision !== revision) return;
                    // Do not automatically retry a mutation after a lost reply.
                    render({status:'failed', instances:[], message:text.unknown}, true);
                }).always(() => setBusy(false));
        }
        button.on('click', () => {
            if (busy) return;
            setBusy(true);
            saveFormToEndpoint('/api/wireguardquic/general/set', 'frm_general_settings', (data) => {
                setBusy(false);
                if (data?.result !== 'saved') {
                    render({status:'failed', instances:[], message:text.saveFailed}, false);
                    return;
                }
                run('/api/wireguardquic/service/reconfigure', {}, true);
            }, true, () => {
                setBusy(false);
                render({status:'failed', instances:[], message:text.saveFailed}, false);
            });
        });
        $(document).on('settings-changed', () => {
            // Any saved edit invalidates an earlier restart confirmation.
            revision++;
            reports = new Map();
            panel.empty();
            $('#change_message_base_form').show();
        });
    }
    function configBuilder(text) {
        const field = (name) => $(document.getElementById('configbuilder.' + name));
        const form = $('#frm_config_builder');
        const save = $('#btn_configbuilder_save').text(text.store);
        field('store_btn').replaceWith(save);
        const next = $('<button type="button" class="btn btn-default"/>').text(text.next).insertAfter(save);
        const notice = $('<div id="builder-notice" role="status" aria-live="polite"/>').insertBefore(save);
        const transport = $('<p id="builder-transport"/>').insertAfter(field('servers'));
        const output = field('output').prop('readonly', true).css({'max-width':'100%', height:'256px'});
        output.closest('tr').find('td:eq(2)').empty().append($('<div id="qrcode"/>'));
        let opened = false, busy = false, saved = false, uncertain = false, serial = 0, instance = null;
        function message(value) { notice.text(value); }
        function update() {
            const ready = instance && !busy && field('privkey').val() && field('pubkey').val() && field('address').val() && field('endpoint').val();
            save.prop('disabled', !ready || saved || uncertain || !field('name').val());
            next.prop('disabled', busy);
            const rows = [];
            if (ready) {
                rows.push('[Interface]', 'PrivateKey = ' + field('privkey').val(), 'Address = ' + field('address').val());
                if (field('peer_dns').val()) rows.push('DNS = ' + field('peer_dns').val());
                if (instance.mtu) rows.push('MTU = ' + instance.mtu);
                for (const key of ['congestion', 'fec', 'obfs']) rows.push('# wg-quic: ' + key + ' = ' + instance[key]);
                rows.push('', '[Peer]', '# wg-quic: peer.fec-latency = balanced', 'PublicKey = ' + instance.pubkey);
                if (field('psk').val()) rows.push('PresharedKey = ' + field('psk').val());
                rows.push('Endpoint = ' + field('endpoint').val(), 'AllowedIPs = ' + field('tunneladdress').val());
                if (field('keepalive').val()) rows.push('PersistentKeepalive = ' + field('keepalive').val());
            }
            // Preserve a completed profile while a save is in flight.
            if (!busy) {
                output.val(rows.join('\n'));
                $('#qrcode').empty();
                if (rows.length) $('#qrcode').qrcode(output.val());
            }
        }
        function lock(value) {
            busy = value;
            form.find('input,select').prop('disabled', value || saved || uncertain);
            $('#pskgen_cb').prop('disabled', value || saved || uncertain);
            form.find('select.selectpicker').selectpicker('refresh');
            update();
        }
        function loadInstance() {
            if (busy) return; // Mapping and selectpicker refresh can emit change while initializing.
            const request = ++serial;
            const id = field('servers').val();
            instance = null;
            transport.empty();
            for (const key of ['address','endpoint','peer_dns']) field(key).val('');
            update();
            form.find('input').prop('disabled', !!id);
            if (!id) { message(text.choose); return; }
            message(text.loading);
            $.ajax({url:'/api/wireguardquic/client/get_server_info/' + encodeURIComponent(id), dataType:'json', timeout:10000})
                .done((data) => {
                    if (request !== serial) return;
                    if (data.status !== 'ok' || !data.pubkey || !data.revision ||
                        !['auto','reno','cubic','model'].includes(data.congestion) ||
                        !['auto','off'].includes(data.fec) || !['none','salamander'].includes(data.obfs)) {
                        message(text.loadFailed); return;
                    }
                    instance = data;
                    for (const key of ['address','endpoint','peer_dns']) field(key).val(data[key]);
                    transport.text(text.inherited + ': congestion=' + data.congestion + ', fec=' + data.fec + ', obfs=' + data.obfs);
                    message(data.address ? '' : text.noAddress);
                    update();
                }).fail(() => { if (request === serial) message(text.loadFailed); })
                .always(() => { if (request === serial) form.find('input').prop('disabled', false); });
        }
        $('#pskgen_cb').on('click', () => {
            if (busy || saved || uncertain) return;
            const request = serial;
            $.ajax({url:'/api/wireguardquic/client/psk', dataType:'json', timeout:10000}).done((data) => {
                if (request === serial && !busy && !saved && !uncertain && data.status === 'ok') field('psk').val(data.psk).trigger('change');
            });
        });
        field('servers').on('change', loadInstance);
        form.on('input change', 'input,select', update);
        save.on('click', () => {
            if (save.prop('disabled')) return;
            clearFormValidation('frm_config_builder');
            const payload = {configbuilder:{enabled:'1', server:field('servers').val(), revision:instance.revision,
                name:field('name').val(), pubkey:field('pubkey').val(), psk:field('psk').val(),
                tunneladdress:field('address').val(), keepalive:field('keepalive').val(), endpoint:field('endpoint').val()}};
            lock(true);
            $.ajax({url:'/api/wireguardquic/client/add_client_builder', type:'POST', data:payload, dataType:'json', timeout:15000})
                .done((data) => {
                    if (data.result === 'saved' && data.uuid) {
                        saved = true;
                        message(text.saved);
                        $(document).trigger('settings-changed');
                    } else if (data.result === 'failed') {
                        const errors = data.validations || {};
                        if (errors['configbuilder.tunneladdress']) {
                            errors['configbuilder.address'] = errors['configbuilder.tunneladdress'];
                            delete errors['configbuilder.tunneladdress'];
                        }
                        handleFormValidation('frm_config_builder', errors);
                        message(text.saveFailed);
                    } else { uncertain = true; message(text.unknown); }
                }).fail(() => { uncertain = true; message(text.unknown); })
                .always(() => lock(false));
        });
        function reset() {
            serial++;
            instance = null;
            saved = false;
            uncertain = false;
            output.val('');
            $('#qrcode').empty();
            transport.empty();
            lock(true);
            message(text.loading);
            mapDataToFormUI({'frm_config_builder':'/api/wireguardquic/client/get_client_builder'}).done((data) => {
                if (!data.frm_config_builder?.configbuilder) { lock(false); message(text.loadFailed); return; }
                formatTokenizersUI();
                form.find('select.selectpicker').selectpicker('refresh');
                field('tunneladdress').val('0.0.0.0/0,::/0');
                field('privkey').val('');
                field('pubkey').val('');
                clearFormValidation('frm_config_builder');
                $.ajax({url:'/api/wireguardquic/server/key_pair', dataType:'json', timeout:10000})
                    .done((data) => {
                        if (data.status === 'ok') { field('pubkey').val(data.pubkey); field('privkey').val(data.privkey); }
                    }).always(() => { lock(false); loadInstance(); });
            }).fail(() => { lock(false); message(text.loadFailed); });
        }
        next.on('click', () => {
            if (!busy) stdDialogConfirm(text.next, text.discard, text.next, text.cancel, reset);
        });
        return {open() { if (!opened) { opened = true; reset(); } }};
    }
    return {peerTransport, applyWorkflow, configBuilder};
})();
