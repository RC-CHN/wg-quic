import { contextBridge, ipcRenderer } from 'electron';
import type { DesktopAPI, TunnelAction } from './types';

const api: DesktopAPI = {
  snapshot: () => ipcRenderer.invoke('desktop:snapshot'),
  manage: (name: string, action: TunnelAction) =>
    ipcRenderer.invoke('desktop:manage', name, action),
  check: (name: string) => ipcRenderer.invoke('desktop:check', name),
  importConfig: () => ipcRenderer.invoke('desktop:import'),
  openConfigDirectory: () =>
    ipcRenderer.invoke('desktop:open-config-directory'),
};

contextBridge.exposeInMainWorld('wgQuic', api);
