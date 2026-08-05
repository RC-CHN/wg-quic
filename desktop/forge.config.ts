import path from 'node:path';
import type { ForgeConfig } from '@electron-forge/shared-types';
import { MakerDeb } from '@electron-forge/maker-deb';
import { MakerWix } from '@electron-forge/maker-wix';
import { MakerZIP } from '@electron-forge/maker-zip';
import { AutoUnpackNativesPlugin } from '@electron-forge/plugin-auto-unpack-natives';
import { FusesPlugin } from '@electron-forge/plugin-fuses';
import { WebpackPlugin } from '@electron-forge/plugin-webpack';
import { FuseV1Options, FuseVersion } from '@electron/fuses';
import { mainConfig } from './webpack.main.config';
import { rendererConfig } from './webpack.renderer.config';

const config: ForgeConfig = {
  packagerConfig: {
    asar: true,
    executableName: 'wg-quic',
    extraResource: [path.resolve(__dirname, 'resources/bin')],
    win32metadata: {
      CompanyName: 'RC-CHN',
      FileDescription: 'wg-quic VPN client',
      InternalName: 'wg-quic-desktop',
      OriginalFilename: 'wg-quic.exe',
      ProductName: 'wg-quic',
      'requested-execution-level': 'asInvoker',
    },
  },
  rebuildConfig: {},
  makers: [
    new MakerWix({
      name: 'wg-quic',
      manufacturer: 'RC-CHN',
      description: 'Desktop shell for wg-quic-quick',
      exe: 'wg-quic.exe',
      arch: 'x64',
      appUserModelId: 'com.rc-chn.wg-quic.desktop',
      defaultInstallMode: 'perMachine',
      features: false,
      programFilesFolderName: 'wg-quic',
      shortcutFolderName: 'wg-quic',
      shortcutName: 'wg-quic',
      upgradeCode: '7a67f9da-0b3f-4a51-9062-7f3b921fec39',
    }),
    new MakerZIP({}, ['linux']),
    new MakerDeb({
      options: {
        productName: 'wg-quic',
        genericName: 'VPN client',
        categories: ['Network'],
        bin: 'wg-quic',
      },
    }),
  ],
  plugins: [
    new AutoUnpackNativesPlugin({}),
    new WebpackPlugin({
      mainConfig,
      renderer: {
        config: rendererConfig,
        entryPoints: [
          {
            html: './src/index.html',
            js: './src/renderer.ts',
            name: 'main_window',
            preload: {
              js: './src/preload.ts',
            },
          },
        ],
      },
    }),
    new FusesPlugin({
      version: FuseVersion.V1,
      [FuseV1Options.RunAsNode]: false,
      [FuseV1Options.EnableCookieEncryption]: true,
      [FuseV1Options.EnableNodeOptionsEnvironmentVariable]: false,
      [FuseV1Options.EnableNodeCliInspectArguments]: false,
      [FuseV1Options.EnableEmbeddedAsarIntegrityValidation]: true,
      [FuseV1Options.OnlyLoadAppFromAsar]: true,
    }),
  ],
};

export default config;
