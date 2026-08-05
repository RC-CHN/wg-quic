import { execFile } from 'node:child_process';
import {
  constants,
  copyFile,
  mkdir,
  readdir,
  chmod,
} from 'node:fs/promises';
import path from 'node:path';
import { promisify } from 'node:util';
import { app } from 'electron';
import {
  defaultConfigDirectory,
  isSupportedPlatform,
  isValidInterfaceName,
} from './paths';
import type {
  BackendInfo,
  CoreStatus,
  DesktopSnapshot,
  PeerStatus,
  RuntimeStats,
  TunnelAction,
  TunnelView,
} from './types';

const execFileAsync = promisify(execFile);
let cachedVersions:
  | Pick<BackendInfo, 'coreVersion' | 'quickVersion'>
  | undefined;

function executableName(base: string): string {
  return process.platform === 'win32' ? `${base}.exe` : base;
}

function bundledBinary(base: string): string {
  const name = executableName(base);
  const explicit =
    base === 'wg-quic'
      ? process.env.WG_QUIC_BIN
      : process.env.WG_QUIC_QUICK_BIN;
  if (explicit) {
    return explicit;
  }
  if (app.isPackaged) {
    return path.join(process.resourcesPath, 'bin', name);
  }
  return path.join(app.getAppPath(), 'resources', 'bin', name);
}

export function configDirectory(): string {
  if (process.env.WG_QUIC_CONFIG_DIR) {
    return path.resolve(process.env.WG_QUIC_CONFIG_DIR);
  }
  return defaultConfigDirectory(process.platform, process.env.ProgramData);
}

function safeError(error: unknown): string {
  if (!(error instanceof Error)) {
    return String(error);
  }
  const candidate = error as Error & { stderr?: string };
  const detail = candidate.stderr?.trim() || error.message;
  return detail.replace(/\s+/g, ' ').slice(0, 800);
}

async function run(
  executable: string,
  args: string[],
  timeout = 8_000,
): Promise<string> {
  const { stdout } = await execFileAsync(executable, args, {
    encoding: 'utf8',
    maxBuffer: 2 * 1024 * 1024,
    timeout,
    windowsHide: true,
  });
  return stdout.trim();
}

function validateName(name: string): void {
  if (!isValidInterfaceName(name, process.platform)) {
    throw new Error(`invalid tunnel name ${JSON.stringify(name)}`);
  }
}

async function profiles(): Promise<Array<{ name: string; configPath: string }>> {
  const directory = configDirectory();
  let entries;
  try {
    entries = await readdir(directory, { withFileTypes: true });
  } catch (error) {
    const candidate = error as NodeJS.ErrnoException;
    if (candidate.code === 'ENOENT') {
      return [];
    }
    throw error;
  }
  return entries
    .filter((entry) => entry.isFile() && entry.name.endsWith('.conf'))
    .map((entry) => ({
      name: entry.name.slice(0, -'.conf'.length),
      configPath: path.join(directory, entry.name),
    }))
    .filter((profile) => isValidInterfaceName(profile.name, process.platform))
    .sort((left, right) => left.name.localeCompare(right.name));
}

async function version(executable: string): Promise<string> {
  return run(executable, ['version']);
}

async function status(name: string): Promise<CoreStatus> {
  validateName(name);
  const output = await run(bundledBinary('wg-quic'), [
    'show',
    name,
    '--json',
  ]);
  let parsed: unknown;
  try {
    parsed = JSON.parse(output);
  } catch (error) {
    throw new Error(`wg-quic returned invalid status JSON: ${safeError(error)}`);
  }
  return normalizeStatus(parsed, name);
}

async function backendInfo(): Promise<BackendInfo> {
  const backend: BackendInfo = {
    platform: process.platform,
    arch: process.arch,
    configDirectory: configDirectory(),
    supported: isSupportedPlatform(process.platform),
  };
  if (cachedVersions) {
    return { ...backend, ...cachedVersions };
  }
  try {
    [backend.coreVersion, backend.quickVersion] = await Promise.all([
      version(bundledBinary('wg-quic')),
      version(bundledBinary('wg-quic-quick')),
    ]);
    cachedVersions = {
      coreVersion: backend.coreVersion,
      quickVersion: backend.quickVersion,
    };
  } catch (error) {
    backend.error = safeError(error);
  }
  return backend;
}

export async function snapshot(): Promise<DesktopSnapshot> {
  const backend = await backendInfo();
  let configured: Array<{ name: string; configPath: string }> = [];
  try {
    configured = await profiles();
  } catch (error) {
    backend.error = safeError(error);
  }

  const tunnels: TunnelView[] = await Promise.all(
    configured.map(async (profile) => {
      try {
        const current = await status(profile.name);
        return {
          ...profile,
          running: true,
          status: current,
        };
      } catch (error) {
        return {
          ...profile,
          running: false,
          statusDetail: safeError(error),
        };
      }
    }),
  );

  return {
    backend,
    tunnels,
    refreshedAt: new Date().toISOString(),
  };
}

async function requireProfile(name: string): Promise<string> {
  validateName(name);
  const profile = (await profiles()).find((candidate) => candidate.name === name);
  if (!profile) {
    throw new Error(`tunnel ${name} is not configured`);
  }
  return profile.configPath;
}

export async function checkTunnel(name: string): Promise<string> {
  const configPath = await requireProfile(name);
  return run(bundledBinary('wg-quic-quick'), ['check', configPath]);
}

export async function manageTunnel(
  name: string,
  action: TunnelAction,
): Promise<void> {
  await requireProfile(name);
  if (action !== 'up' && action !== 'down') {
    throw new Error(`unsupported tunnel action ${JSON.stringify(action)}`);
  }
  await run(bundledBinary('wg-quic-quick'), [action, name], 40_000);
}

export async function importConfig(
  sourcePath: string,
  overwrite: boolean,
): Promise<string> {
  if (path.extname(sourcePath).toLowerCase() !== '.conf') {
    throw new Error('wg-quic configuration files must use the .conf extension');
  }
  const name = path.basename(sourcePath, '.conf');
  validateName(name);
  await run(bundledBinary('wg-quic-quick'), ['check', sourcePath]);

  const directory = configDirectory();
  const destination = path.join(directory, `${name}.conf`);
  await mkdir(directory, { recursive: true, mode: 0o700 });
  await copyFile(
    sourcePath,
    destination,
    overwrite ? 0 : constants.COPYFILE_EXCL,
  );
  if (process.platform !== 'win32') {
    await chmod(destination, 0o600);
  }
  return name;
}

export function describeError(error: unknown): string {
  return safeError(error);
}

function record(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === 'object'
    ? (value as Record<string, unknown>)
    : {};
}

function stringField(
  value: Record<string, unknown>,
  key: string,
  fallback = '',
): string {
  return typeof value[key] === 'string' ? value[key] : fallback;
}

function numberField(value: Record<string, unknown>, key: string): number {
  const candidate = value[key];
  return typeof candidate === 'number' && Number.isFinite(candidate)
    ? candidate
    : 0;
}

function normalizeStats(value: unknown): RuntimeStats {
  const candidate = record(value);
  return {
    wg_tx_packets: numberField(candidate, 'wg_tx_packets'),
    wg_tx_bytes: numberField(candidate, 'wg_tx_bytes'),
    wg_rx_packets: numberField(candidate, 'wg_rx_packets'),
    wg_rx_bytes: numberField(candidate, 'wg_rx_bytes'),
    wire_tx_packets: numberField(candidate, 'wire_tx_packets'),
    wire_tx_bytes: numberField(candidate, 'wire_tx_bytes'),
    wire_rx_packets: numberField(candidate, 'wire_rx_packets'),
    wire_rx_bytes: numberField(candidate, 'wire_rx_bytes'),
    queue_drops: numberField(candidate, 'queue_drops'),
    fec_data_tx: numberField(candidate, 'fec_data_tx'),
    fec_parity_tx: numberField(candidate, 'fec_parity_tx'),
    fec_raw_lost: numberField(candidate, 'fec_raw_lost'),
    fec_recovered: numberField(candidate, 'fec_recovered'),
    fec_unrecovered: numberField(candidate, 'fec_unrecovered'),
    fec_current_parity_shards: numberField(
      candidate,
      'fec_current_parity_shards',
    ),
    fec_loss_estimate_ppm: numberField(candidate, 'fec_loss_estimate_ppm'),
    active_sessions: numberField(candidate, 'active_sessions'),
    quic_smoothed_rtt_us: numberField(candidate, 'quic_smoothed_rtt_us'),
    quic_bandwidth_estimate_bps: numberField(
      candidate,
      'quic_bandwidth_estimate_bps',
    ),
    quic_pacing_rate_bps: numberField(candidate, 'quic_pacing_rate_bps'),
    quic_queue_delay_us: numberField(candidate, 'quic_queue_delay_us'),
  };
}

function normalizePeer(value: unknown): PeerStatus | undefined {
  const candidate = record(value);
  const publicKey = stringField(candidate, 'public_key');
  if (!publicKey) {
    return undefined;
  }
  return {
    public_key: publicKey,
    endpoint: stringField(candidate, 'endpoint') || undefined,
    generation: numberField(candidate, 'generation'),
    session: stringField(candidate, 'session', 'unknown'),
  };
}

function normalizeStatus(value: unknown, expectedName: string): CoreStatus {
  const candidate = record(value);
  const interfaceName = stringField(candidate, 'interface');
  if (interfaceName !== expectedName) {
    throw new Error(
      `wg-quic returned status for ${JSON.stringify(interfaceName || 'unknown')} instead of ${JSON.stringify(expectedName)}`,
    );
  }
  const peers = Array.isArray(candidate.peers)
    ? candidate.peers
        .map((peer) => normalizePeer(peer))
        .filter((peer): peer is PeerStatus => peer !== undefined)
    : [];
  return {
    interface: interfaceName,
    state: stringField(candidate, 'state', 'unknown'),
    listen_port: numberField(candidate, 'listen_port'),
    carrier: stringField(candidate, 'carrier', 'quic'),
    fec_mode: stringField(candidate, 'fec_mode', 'unknown'),
    obfs_mode: stringField(candidate, 'obfs_mode', 'unknown'),
    peers,
    stats: normalizeStats(candidate.stats),
  };
}
