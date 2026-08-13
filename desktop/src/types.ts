export type TunnelAction = 'up' | 'down';

export interface PeerStatus {
  public_key: string;
  endpoint?: string;
  generation: number;
  session: string;
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
  interface: string;
  state: string;
  listen_port: number;
  carrier: string;
  fec_mode: string;
  obfs_mode: string;
  addresses?: string[];
  peers?: PeerStatus[];
  stats: RuntimeStats;
}

export interface TunnelView {
  name: string;
  configPath: string;
  running: boolean;
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

export interface DesktopAPI {
  snapshot(): Promise<DesktopSnapshot>;
  manage(name: string, action: TunnelAction): Promise<DesktopSnapshot>;
  check(name: string): Promise<string>;
  deleteTunnel(name: string): Promise<DeleteResult>;
  importConfig(): Promise<ImportResult>;
  importConfigPath(
    sourcePath: string,
    overwrite: boolean,
  ): Promise<ImportResult>;
  openConfigDirectory(): Promise<string>;
}
