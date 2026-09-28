import { t } from './i18n';
import { invoke } from '@tauri-apps/api/core';
import { ask, open, save } from '@tauri-apps/plugin-dialog';
import type {
  ApplyResult,
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
  collectDiagnostics: async (name, peer) => {
    const destination = await save({ title: t('Save diagnostics to a new ZIP file'), defaultPath: `wg-quic-${name}-${Date.now()}.zip`, filters: [{name: t('ZIP archive'), extensions: ['zip']}] });
    if (!destination) return { canceled: true };
    return invoke('collect_diagnostics', { name, peer, destination });
  },
  confirmRestart: (name) => ask(t("Restart \"{0}\" to apply the saved configuration? Traffic will be interrupted while the tunnel reconnects.", name), {
    title: t('Restart tunnel?'), kind: 'warning', okLabel: t('Restart'), cancelLabel: t('Keep running'),
  }),
  apply: (name, requestId) => invoke<ApplyResult>('apply_tunnel', { name, requestId }),
  confirmRegenerate: () => ask(t('Generate a new private key? After saving, the other peer must use your new public key to reconnect.'), {
    title: t('Replace tunnel identity?'), kind: 'warning', okLabel: t('Generate new key'), cancelLabel: t('Keep existing key'),
  }),
  confirmDiscard: () => ask(t('Discard unsaved changes to this configuration?'), {
    title: t('Unsaved changes'), kind: 'warning', okLabel: t('Discard'), cancelLabel: t('Keep editing'),
  }),
  snapshot: (selectedName, force = false) => invoke<DesktopSnapshot>('snapshot', { selectedName, force }),
  manage: (name: string, action: TunnelAction) =>
    invoke<DesktopSnapshot>('manage_tunnel', { name, action }),
  check: (name: string) => invoke<string>('check_tunnel', { name }),
  deleteTunnel: async (name: string): Promise<DeleteResult> => {
    const confirmed = await ask(
      t("Delete tunnel \"{0}\"? This stops the tunnel and removes its configuration. This cannot be undone.", name),
      {
        title: t('Delete tunnel?'),
        kind: 'warning',
        okLabel: t('Delete'),
        cancelLabel: t('Cancel'),
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
  derivePublicKey: (privateKey) => invoke<string>('derive_public_key', { privateKey }),
  writeTunnel: (name: string, contents: string, overwrite: boolean) =>
    invoke<DesktopSnapshot>('write_tunnel', { name, contents, overwrite }),
  importConfig: async () => {
    const selected = await open({
      title: t('Import wg-quic configuration'),
      multiple: false,
      directory: false,
      filters: [{ name: t('wg-quic configuration'), extensions: ['conf'] }],
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
        t("{0} already exists. Replacing a running tunnel configuration does not restart it automatically.", selected),
        {
          title: t('Replace tunnel configuration?'),
          kind: 'warning',
          okLabel: t('Replace'),
          cancelLabel: t('Cancel'),
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

export const quitDesktop = (): Promise<void> => invoke('quit_desktop');
