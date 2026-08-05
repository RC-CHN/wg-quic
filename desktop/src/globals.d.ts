import type { DesktopAPI } from './types';

declare global {
  const MAIN_WINDOW_WEBPACK_ENTRY: string;
  const MAIN_WINDOW_PRELOAD_WEBPACK_ENTRY: string;

  interface Window {
    wgQuic: DesktopAPI;
  }
}

export {};
