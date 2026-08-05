<script>
$(document).ready(function() {
    const statusGrid = $('#grid-wireguardquic-status').UIBootgrid({
        search: '/api/wireguardquic/service/show',
        options: {
            multiSelect: false,
            rowSelect: false,
            selection: false,
            formatters: {
                bytes: function(column, row) {
                    return row[column.id] > 0 ? byteFormat(row[column.id], 2) : '';
                },
                seconds: function(column, row) {
                    return row[column.id] !== null ? row[column.id] + 's' : '';
                },
                epoch: function(column, row) {
                    return row[column.id] !== null ? row[column.id] : '';
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

<div class="tab-content content-box">
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
                <th data-column-id="type" data-type="string" data-width="6em">{{ lang._('Type') }}</th>
                <th data-column-id="public-key" data-type="string" data-identifier="true" data-visible="false">{{ lang._('Public key') }}</th>
                <th data-column-id="name" data-type="string">{{ lang._('Name') }}</th>
                <th data-column-id="endpoint" data-type="string">{{ lang._('Port / Endpoint') }}</th>
                <th data-column-id="allowed-ips" data-type="string">{{ lang._('Allowed IPs') }}</th>
                <th data-column-id="session" data-type="string">{{ lang._('QUIC Session') }}</th>
                <th data-column-id="transfer-tx" data-formatter="bytes" data-type="numeric">{{ lang._('Sent') }}</th>
                <th data-column-id="transfer-rx" data-formatter="bytes" data-type="numeric">{{ lang._('Received') }}</th>
            </tr>
        </thead>
        <tbody></tbody>
    </table>
</div>
