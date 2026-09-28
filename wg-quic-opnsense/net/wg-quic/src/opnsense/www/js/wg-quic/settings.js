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
    return {peerTransport};
})();
