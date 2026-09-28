<script>
$(document).ready(function() {
    const sessionLabels = {
        established: {{ lang._('Connected') | json_encode() }},
        dialing: {{ lang._('Connecting') | json_encode() }},
        reconnecting: {{ lang._('Reconnecting') | json_encode() }},
        idle: {{ lang._('Waiting') | json_encode() }},
        closed: {{ lang._('Closed') | json_encode() }}
    };
    const escapeText = (value) => $('<span/>').text(value).html();
    function timestamp(value) {
        if (!value || !Number.isFinite(Number(value))) return '—';
        const date = new Date(Number(value) * 1000);
        return Number.isFinite(date.getTime()) ? date.toLocaleString(document.documentElement.lang, {hour12: false}) : '—';
    }
    const statusGrid = $('#grid-wireguardquic-status').UIBootgrid({
        search: '/api/wireguardquic/service/show',
        options: {
            multiSelect: false,
            rowSelect: false,
            selection: false,
            formatters: {
                bytes: function(column, row) {
                    return typeof row[column.id] === 'number' ? (row[column.id] === 0 ? '0 B' : byteFormat(row[column.id], 2)) : '—';
                },
                seconds: function(column, row) {
                    return row[column.id] !== null ? row[column.id] + 's' : '';
                },
                epoch: function(column, row) {
                    return timestamp(row[column.id.replace(/-epoch$/, '')]);
                },
                activity: function(column, row) {
                    if (!row['last-activity']) {
                        return '—';
                    }
                    let direction = row['last-activity-direction'];
                    if (direction === 'received') {
                        direction = "{{ lang._('received') }}";
                    } else if (direction === 'sent') {
                        direction = "{{ lang._('sent') }}";
                    }
                    return direction
                        ? timestamp(row['last-activity']) + ' · ' + escapeText(direction)
                        : timestamp(row['last-activity']);
                },
                type: function(column, row) {
                    return row.type === 'interface' ? {{ lang._('Instance') | json_encode() }} : {{ lang._('Peer') | json_encode() }};
                },
                session: function(column, row) {
                    if (!row.session) return '—';
                    const label = sessionLabels[row.session] || {{ lang._('Unknown') | json_encode() }};
                    if (row.session === 'reconnecting' && row['next-reconnect']) {
                        return label + '<br><small>' + timestamp(row['next-reconnect']) + '</small>';
                    }
                    return label;
                },
                status: function(column, row) {
                    if (row.type === 'interface' && row.status === 'up') {
                        return '<span class="fa fa-check-circle fa-fw text-success" data-toggle="tooltip" title="{{ lang._('Online') }}"></span>';
                    }
                    if (row.type === 'peer' && row['peer-status'] === 'online') {
                        return '<span class="fa fa-check-circle fa-fw text-success" data-toggle="tooltip" title="{{ lang._('Online') }}"></span>';
                    }
                    if (row.type === 'peer' && row['peer-status'] === 'stale') {
                        return '<span class="fa fa-question-circle fa-fw" data-toggle="tooltip" title="{{ lang._('Stale') }}"></span>';
                    }
                    return '<span class="fa fa-times-circle fa-fw text-danger" data-toggle="tooltip" title="{{ lang._('Offline') }}"></span>';
                }
            },
            requestHandler: function(request) {
                if ($('#type_filter').val().length > 0) {
                    request.type = $('#type_filter').val();
                }
                return request;
            }
        }
    });

    $('#type_filter').change(function() {
        $('#grid-wireguardquic-status').bootgrid('reload');
    });

    statusGrid.on('loaded.rs.jquery.bootgrid', function() {
        $('[data-toggle="tooltip"]').tooltip();
    });

    $('#type_filter_container').detach().insertAfter('#grid-wireguardquic-status-header .search');
});
</script>

<div class="tab-content content-box wg-quic-status">
    <div class="hidden">
        <div id="type_filter_container" class="btn-group">
            <select id="type_filter" data-title="{{ lang._('Type') }}"
                    class="selectpicker" multiple="multiple" data-width="200px">
                <option value="interface">{{ lang._('Instance') }}</option>
                <option value="peer">{{ lang._('Peer') }}</option>
            </select>
        </div>
    </div>
    <table id="grid-wireguardquic-status" class="table table-condensed table-hover table-striped table-responsive">
        <thead>
            <tr>
                <th data-column-id="status" data-formatter="status" data-type="string" data-width="6em">{{ lang._('Status') }}</th>
                <th data-column-id="if" data-type="string" data-width="6em">{{ lang._('Device') }}</th>
                <th data-column-id="type" data-formatter="type" data-type="string" data-width="6em">{{ lang._('Type') }}</th>
                <th data-column-id="public-key" data-type="string" data-identifier="true" data-visible="false">{{ lang._('Public key') }}</th>
                <th data-column-id="name" data-type="string">{{ lang._('Name') }}</th>
                <th data-column-id="endpoint" data-type="string">{{ lang._('Port / Current endpoint') }}</th>
                <th data-column-id="allowed-ips" data-visible="false" data-type="string">{{ lang._('Allowed IPs') }}</th>
                <th data-column-id="session" data-formatter="session" data-type="string">{{ lang._('QUIC Session') }}</th>
                <th data-column-id="reconnect-attempts" data-visible="false" data-type="numeric">{{ lang._('Reconnect attempts') }}</th>
                <th data-column-id="reconnect-failures" data-visible="false" data-type="numeric">{{ lang._('Reconnect failures') }}</th>
                <th data-column-id="last-activity-epoch" data-visible="false" data-formatter="activity" data-type="string">{{ lang._('Last activity') }}</th>
                <th data-column-id="latest-handshake-epoch" data-formatter="epoch" data-type="string">{{ lang._('Latest handshake') }}</th>
                <th data-column-id="transfer-tx" data-formatter="bytes" data-type="numeric">{{ lang._('Sent') }}</th>
                <th data-column-id="transfer-rx" data-formatter="bytes" data-type="numeric">{{ lang._('Received') }}</th>
            </tr>
        </thead>
        <tbody></tbody>
    </table>
</div>
