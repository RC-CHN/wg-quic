export function isSupportedPlatform(platform: NodeJS.Platform): boolean {
  return platform === 'linux' || platform === 'win32';
}

export function defaultConfigDirectory(
  platform: NodeJS.Platform,
  programData?: string,
): string {
  if (platform === 'win32') {
    return `${programData || 'C:\\ProgramData'}\\wg-quic\\interfaces`;
  }
  if (platform === 'linux') {
    return '/etc/wg-quic';
  }
  throw new Error(`unsupported desktop platform ${platform}`);
}

export function isValidInterfaceName(
  name: string,
  platform: NodeJS.Platform,
): boolean {
  if (platform === 'win32') {
    return (
      /^[^\\/:*?"<>|\u0000-\u001f]{1,128}$/.test(name) &&
      !name.endsWith(' ') &&
      !name.endsWith('.')
    );
  }
  return /^[A-Za-z0-9_=+.-]{1,15}$/.test(name);
}
