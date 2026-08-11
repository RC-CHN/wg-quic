import type { DesktopAPI } from './types';

declare global {
  interface Window {
    wgQuic: DesktopAPI;
  }
}

export {};
