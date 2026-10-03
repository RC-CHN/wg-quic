export type TunnelAction = 'up' | 'down';

export interface PeerStatus {
  public_key: string;
  endpoint?: string;
  generation: number;
  session: string;
  authenticated_endpoint_generation?: number;
  latest_handshake?: number;
  last_rx?: number;
  last_tx?: number;
  reconnect_attempts?: number;
  next_reconnect?: number;
  transfer_rx?: number;
  transfer_tx?: number;
}

export interface RuntimeStats {
  wg_tx_packets: number;
  wg_tx_bytes: number;
  wg_rx_packets: number;
  wg_rx_bytes: number;
  wire_tx_packets: number;
  wire_tx_bytes: number;
  wire_rx_packets: number;
  wire_rx_bytes: number;
  queue_drops: number;
  send_queue_bytes?: number;
  send_queue_delay_max_us?: number;
  send_queue_expired?: number;
  quic_datagram_send_queue_bytes?: number;
  quic_datagram_send_queue_age_us?: number;
  fec_data_tx: number;
  fec_parity_tx: number;
  fec_raw_lost: number;
  fec_recovered: number;
  fec_unrecovered: number;
  fec_current_parity_shards: number;
  fec_loss_estimate_ppm: number;
  active_sessions: number;
  quic_smoothed_rtt_us: number;
  quic_bandwidth_estimate_bps: number;
  quic_pacing_rate_bps: number;
  quic_queue_delay_us: number;
}

export interface CoreStatus {
	public_key?: string;
	observation_id?: string;
  interface: string;
  state: string;
  listen_port: number;
  carrier: string;
  fec_mode: string;
  obfs_mode: string;
  addresses?: string[];
  peers?: PeerStatus[];
  sessions?: Array<{session_id: number; session_generation: number; state: string; peers?: Array<{public_key: string; authenticated: boolean}>}>;
  recent_sessions?: Array<{session_id: number; closed_at: string; close_reason: string; last_error?: string; current_endpoint?: string}>;
  stats: RuntimeStats;
}

export interface TunnelView {
  name: string;
  configPath: string;
  running: boolean;
  statusState?: 'up' | 'prepared' | 'inactive' | 'unknown';
  statusCode?: string;
  sampledAt?: number;
  status?: CoreStatus;
  statusDetail?: string;
}

export interface BackendInfo {
  platform: string;
  arch: string;
  configDirectory: string;
  supported: boolean;
  coreVersion?: string;
  quickVersion?: string;
  managementStatus?:
    | 'ready'
    | 'unauthorized'
    | 'unavailable'
    | 'incompatible'
    | 'error';
  managementMessage?: string;
  error?: string;
}

export interface DesktopSnapshot {
  backend: BackendInfo;
  tunnels: TunnelView[];
  refreshedAt: string;
}

export interface ImportResult {
  canceled: boolean;
  importedName?: string;
  snapshot: DesktopSnapshot;
}

export interface DeleteResult {
  canceled: boolean;
  snapshot: DesktopSnapshot;
}

export interface TunnelKeys {
  private_key: string;
  public_key: string;
}

export interface ApplyResult {
  state: 'applied' | 'restart_required' | 'failed' | 'unknown';
  code?: string;
  message?: string;
  request_id?: string;
  restart_reasons?: string[];
  cleanup_pending?: boolean;
}

export interface DesktopAPI {
  collectDiagnostics(name: string, peer: string): Promise<{canceled: boolean; path?: string; complete?: boolean; detail?: string}>;
  confirmDiscard(): Promise<boolean>;
  confirmRegenerate(): Promise<boolean>;
  confirmRestart(name: string): Promise<boolean>;
  apply(name: string, requestId?: string): Promise<ApplyResult>;
  snapshot(selectedName?: string, force?: boolean): Promise<DesktopSnapshot>;
  manage(name: string, action: TunnelAction): Promise<DesktopSnapshot>;
  check(name: string): Promise<string>;
  deleteTunnel(name: string): Promise<DeleteResult>;
  readTunnel(name: string): Promise<string>;
  generateKeys(): Promise<TunnelKeys>;
  derivePublicKey(privateKey: string): Promise<string>;
  writeTunnel(
    name: string,
    contents: string,
    overwrite: boolean,
  ): Promise<DesktopSnapshot>;
  importConfig(): Promise<ImportResult>;
  importConfigPath(
    sourcePath: string,
    overwrite: boolean,
  ): Promise<ImportResult>;
  openConfigDirectory(): Promise<string>;
}
