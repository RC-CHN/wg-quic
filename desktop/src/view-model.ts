import type { TunnelAction, TunnelView } from './types';

export type TunnelDisplayState =
  | 'active'
  | 'inactive'
  | 'activating'
  | 'deactivating';

export function tunnelDisplayState(
  tunnel: TunnelView,
  pending?: TunnelAction,
): TunnelDisplayState {
  if (pending === 'up') {
    return 'activating';
  }
  if (pending === 'down') {
    return 'deactivating';
  }
  return tunnel.running ? 'active' : 'inactive';
}

export function tunnelStateLabel(state: TunnelDisplayState): string {
  switch (state) {
    case 'active':
      return 'Active';
    case 'inactive':
      return 'Inactive';
    case 'activating':
      return 'Activating…';
    case 'deactivating':
      return 'Deactivating…';
  }
}

export function chooseSelectedTunnel(
  tunnels: TunnelView[],
  selectedName?: string,
): string | undefined {
  if (
    selectedName &&
    tunnels.some((candidate) => candidate.name === selectedName)
  ) {
    return selectedName;
  }
  return tunnels[0]?.name;
}

export function formatBytes(value = 0): string {
  if (!Number.isFinite(value) || value <= 0) {
    return '0 B';
  }
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  const exponent = Math.min(
    Math.floor(Math.log(value) / Math.log(1024)),
    units.length - 1,
  );
  const scaled = value / 1024 ** exponent;
  return `${scaled >= 100 || exponent === 0 ? scaled.toFixed(0) : scaled.toFixed(1)} ${units[exponent]}`;
}

export function formatBitRate(value = 0): string {
  if (!Number.isFinite(value) || value <= 0) {
    return '—';
  }
  const units = ['bps', 'Kbps', 'Mbps', 'Gbps', 'Tbps'];
  const exponent = Math.min(
    Math.floor(Math.log(value) / Math.log(1000)),
    units.length - 1,
  );
  const scaled = value / 1000 ** exponent;
  return `${scaled >= 100 || exponent === 0 ? scaled.toFixed(0) : scaled.toFixed(1)} ${units[exponent]}`;
}

export function formatRTT(microseconds = 0): string {
  return microseconds > 0
    ? `${(microseconds / 1000).toFixed(microseconds < 10_000 ? 1 : 0)} ms`
    : '—';
}

export function formatFECRecovery(recovered = 0, rawLost = 0): string {
  if (rawLost <= 0) {
    return 'No observed loss';
  }
  return `${((recovered / rawLost) * 100).toFixed(1)}% recovered`;
}
