import { invoke } from '@tauri-apps/api/core';
import { ask, open } from '@tauri-apps/plugin-dialog';
import type {
  DeleteResult,
  DesktopAPI,
  DesktopSnapshot,
  ImportResult,
  TunnelAction,
  TunnelKeys,
} from './types';

interface DesktopSmokeSettings {
  mode: 'none' | 'renderer' | 'integration' | 'tray';
  source?: string;
  name?: string;
}

function errorMessage(error: unknown): string {
  if (typeof error === 'string') {
    return error;
  }
  if (error instanceof Error) {
    return error.message;
  }
  return String(error);
}

async function importConfigPath(
  sourcePath: string,
  overwrite: boolean,
): Promise<ImportResult> {
  return invoke<ImportResult>('import_config', { sourcePath, overwrite });
}

const api: DesktopAPI = {
  snapshot: () => invoke<DesktopSnapshot>('snapshot'),
  manage: (name: string, action: TunnelAction) =>
    invoke<DesktopSnapshot>('manage_tunnel', { name, action }),
  check: (name: string) => invoke<string>('check_tunnel', { name }),
  deleteTunnel: async (name: string): Promise<DeleteResult> => {
    const confirmed = await ask(
      `Delete tunnel "${name}"? This stops the tunnel and removes its configuration. This cannot be undone.`,
      {
        title: 'Delete tunnel?',
        kind: 'warning',
        okLabel: 'Delete',
        cancelLabel: 'Cancel',
      },
    );
    if (!confirmed) {
      return { canceled: true, snapshot: await api.snapshot() };
    }
    return {
      canceled: false,
      snapshot: await invoke<DesktopSnapshot>('delete_tunnel', { name }),
    };
  },
  readTunnel: (name: string) => invoke<string>('read_tunnel', { name }),
  generateKeys: async (): Promise<TunnelKeys> => {
    const raw = await invoke<string>('generate_keys');
    return JSON.parse(raw) as TunnelKeys;
  },
  writeTunnel: (name: string, contents: string, overwrite: boolean) =>
    invoke<DesktopSnapshot>('write_tunnel', { name, contents, overwrite }),
  importConfig: async () => {
    const selected = await open({
      title: 'Import wg-quic configuration',
      multiple: false,
      directory: false,
      filters: [{ name: 'wg-quic configuration', extensions: ['conf'] }],
    });
    if (!selected) {
      return { canceled: true, snapshot: await api.snapshot() };
    }
    try {
      return await importConfigPath(selected, false);
    } catch (error) {
      const message = errorMessage(error);
      if (!/file exists|already exists/i.test(message)) {
        throw error;
      }
      const replace = await ask(
        `${selected} already exists. Replacing a running tunnel configuration does not restart it automatically.`,
        {
          title: 'Replace tunnel configuration?',
          kind: 'warning',
          okLabel: 'Replace',
          cancelLabel: 'Cancel',
        },
      );
      if (!replace) {
        return { canceled: true, snapshot: await api.snapshot() };
      }
      return importConfigPath(selected, true);
    }
  },
  importConfigPath,
  openConfigDirectory: () => invoke<string>('open_config_directory'),
};

window.wgQuic = api;

export function desktopSmokeSettings(): Promise<DesktopSmokeSettings> {
  return invoke<DesktopSmokeSettings>('desktop_smoke_settings');
}

export function completeDesktopSmoke(
  message: string,
  failed = false,
): Promise<void> {
  return invoke('complete_desktop_smoke', { message, failed });
}

export function reportDesktopSmoke(message: string): Promise<void> {
  return invoke('report_desktop_smoke', { message });
}
