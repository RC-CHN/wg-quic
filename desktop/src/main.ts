import path from 'node:path';
import {
  app,
  BrowserWindow,
  Menu,
  nativeImage,
  dialog,
  ipcMain,
  session,
  shell,
  Tray,
} from 'electron';
import started from 'electron-squirrel-startup';
import {
  checkTunnel,
  configDirectory,
  describeError,
  importConfig,
  manageTunnel,
  snapshot,
} from './cli';
import type { TunnelAction } from './types';

if (started) {
  app.quit();
}

app.enableSandbox();

let mainWindow: BrowserWindow | null = null;
let tray: Tray | null = null;
let quitting = false;
const smokeMode = process.env.WG_QUIC_DESKTOP_SMOKE === '1';

const trayIconSVG = `
  <svg xmlns="http://www.w3.org/2000/svg" width="32" height="32" viewBox="0 0 32 32">
    <rect width="32" height="32" rx="8" fill="#10151d"/>
    <path d="M7 21V16M12 24V10M17 21V14M22 24V8M27 19V13"
      stroke="#59e391" stroke-width="3" stroke-linecap="round"/>
  </svg>`;

function requireWindowSender(event: Electron.IpcMainInvokeEvent): void {
  if (!mainWindow || event.sender !== mainWindow.webContents) {
    throw new Error('rejected desktop IPC from an unknown renderer');
  }
}

function registerIPC(): void {
  ipcMain.handle('desktop:snapshot', async (event) => {
    requireWindowSender(event);
    return snapshot();
  });

  ipcMain.handle(
    'desktop:manage',
    async (event, name: unknown, action: unknown) => {
      requireWindowSender(event);
      if (
        typeof name !== 'string' ||
        (action !== 'up' && action !== 'down')
      ) {
        throw new Error('invalid tunnel action');
      }
      await manageTunnel(name, action as TunnelAction);
      return snapshot();
    },
  );

  ipcMain.handle('desktop:check', async (event, name: unknown) => {
    requireWindowSender(event);
    if (typeof name !== 'string') {
      throw new Error('invalid tunnel name');
    }
    return checkTunnel(name);
  });

  ipcMain.handle('desktop:import', async (event) => {
    requireWindowSender(event);
    if (!mainWindow) {
      throw new Error('desktop window is unavailable');
    }
    const selection = await dialog.showOpenDialog(mainWindow, {
      title: 'Import wg-quic configuration',
      buttonLabel: 'Validate and import',
      properties: ['openFile'],
      filters: [{ name: 'wg-quic configuration', extensions: ['conf'] }],
    });
    if (selection.canceled || selection.filePaths.length !== 1) {
      return { canceled: true, snapshot: await snapshot() };
    }

    const sourcePath = selection.filePaths[0];
    if (!sourcePath) {
      return { canceled: true, snapshot: await snapshot() };
    }

    let importedName: string;
    try {
      importedName = await importConfig(sourcePath, false);
    } catch (error) {
      const candidate = error as NodeJS.ErrnoException;
      if (candidate.code !== 'EEXIST') {
        throw new Error(describeError(error));
      }
      const answer = await dialog.showMessageBox(mainWindow, {
        type: 'warning',
        title: 'Replace tunnel configuration?',
        message: `${path.basename(sourcePath)} already exists.`,
        detail:
          'Replacing a running tunnel configuration does not restart it automatically.',
        buttons: ['Cancel', 'Replace'],
        defaultId: 0,
        cancelId: 0,
      });
      if (answer.response !== 1) {
        return { canceled: true, snapshot: await snapshot() };
      }
      importedName = await importConfig(sourcePath, true);
    }
    return {
      canceled: false,
      importedName,
      snapshot: await snapshot(),
    };
  });

  ipcMain.handle('desktop:open-config-directory', async (event) => {
    requireWindowSender(event);
    return shell.openPath(configDirectory());
  });
}

function showMainWindow(): void {
  if (!mainWindow) {
    return;
  }
  if (mainWindow.isMinimized()) {
    mainWindow.restore();
  }
  mainWindow.show();
  mainWindow.focus();
}

function createTray(): void {
  if (smokeMode || process.platform !== 'win32' || tray) {
    return;
  }
  const icon = nativeImage
    .createFromDataURL(
      `data:image/svg+xml;charset=utf-8,${encodeURIComponent(trayIconSVG)}`,
    )
    .resize({ width: 16, height: 16 });
  tray = new Tray(icon);
  tray.setToolTip('wg-quic');
  tray.setContextMenu(
    Menu.buildFromTemplate([
      {
        label: 'Open wg-quic',
        click: showMainWindow,
      },
      { type: 'separator' },
      {
        label: 'Quit',
        click: () => {
          quitting = true;
          app.quit();
        },
      },
    ]),
  );
  tray.on('double-click', showMainWindow);
}

async function verifySmokeWindow(window: BrowserWindow): Promise<void> {
  for (let attempt = 0; attempt < 100; attempt += 1) {
    const state = (await window.webContents.executeJavaScript(`
      ({
        ready: document.body.dataset.ready === 'true',
        title: document.title,
        hasTunnelList: Boolean(document.querySelector('#tunnel-list')),
        hasImportButton: Boolean(document.querySelector('#import-config')),
        hasToggleButton: Boolean(document.querySelector('#toggle-tunnel'))
      })
    `)) as {
      ready: boolean;
      title: string;
      hasTunnelList: boolean;
      hasImportButton: boolean;
      hasToggleButton: boolean;
    };
    if (
      state.ready &&
      state.title === 'wg-quic' &&
      state.hasTunnelList &&
      state.hasImportButton &&
      state.hasToggleButton
    ) {
      console.log('wg-quic desktop renderer smoke test passed');
      app.exit(0);
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error('desktop renderer did not become ready within 10 seconds');
}

async function createWindow(): Promise<void> {
  mainWindow = new BrowserWindow({
    title: 'wg-quic',
    width: 1180,
    height: 760,
    minWidth: 920,
    minHeight: 620,
    backgroundColor: '#0a0d12',
    show: false,
    autoHideMenuBar: true,
    webPreferences: {
      preload: MAIN_WINDOW_PRELOAD_WEBPACK_ENTRY,
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webSecurity: true,
      devTools: !app.isPackaged,
      spellcheck: false,
    },
  });

  mainWindow.webContents.setWindowOpenHandler(() => ({ action: 'deny' }));
  mainWindow.webContents.on('will-attach-webview', (event) => {
    event.preventDefault();
  });
  mainWindow.webContents.on('will-navigate', (event, url) => {
    if (url !== mainWindow?.webContents.getURL()) {
      event.preventDefault();
    }
  });
  if (!smokeMode) {
    mainWindow.once('ready-to-show', () => mainWindow?.show());
  }
  mainWindow.on('close', (event) => {
    if (process.platform === 'win32' && tray && !quitting) {
      event.preventDefault();
      mainWindow?.hide();
    }
  });
  mainWindow.on('closed', () => {
    mainWindow = null;
  });
  await mainWindow.loadURL(MAIN_WINDOW_WEBPACK_ENTRY);
  if (smokeMode) {
    await verifySmokeWindow(mainWindow);
  }
}

const singleInstance = app.requestSingleInstanceLock();
if (!singleInstance) {
  app.quit();
} else {
  app.on('second-instance', () => {
    showMainWindow();
  });

  void app
    .whenReady()
    .then(async () => {
      session.defaultSession.setPermissionRequestHandler(
        (_webContents, _permission, callback) => callback(false),
      );
      registerIPC();
      await createWindow();
      if (smokeMode) {
        return;
      }
      createTray();
    })
    .catch((error: unknown) => {
      if (smokeMode) {
        console.error(`wg-quic desktop smoke test failed: ${describeError(error)}`);
        app.exit(1);
        return;
      }
      dialog.showErrorBox('Unable to start wg-quic', describeError(error));
      app.quit();
    });
}

app.on('window-all-closed', () => {
  app.quit();
});

app.on('before-quit', () => {
  quitting = true;
});
