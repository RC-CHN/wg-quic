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
    return {peerTransport, applyWorkflow};
})();
