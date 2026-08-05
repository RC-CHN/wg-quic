import { execFile } from 'node:child_process';
import { randomUUID } from 'node:crypto';
import net from 'node:net';
import path from 'node:path';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);

export type WindowsElevatedAction = 'up' | 'down' | 'check' | 'import';

export interface WindowsElevationRequest {
  action: WindowsElevatedAction;
  name: string;
  source?: string;
  overwrite?: boolean;
}

interface WindowsElevationResult {
  success: boolean;
  message: string;
}

export function windowsElevationError(
  message: string,
  request: WindowsElevationRequest,
): NodeJS.ErrnoException {
  const normalized = message.trim();
  const error = new Error(
    /operation was canceled|cancelled by the user|error_cancelled|1223/i.test(
      normalized,
    )
      ? 'Administrator approval was canceled.'
      : normalized || 'The elevated wg-quic operation failed.',
  ) as NodeJS.ErrnoException;
  if (
    request.action === 'import' &&
    /file exists|already exists/i.test(normalized)
  ) {
    error.code = 'EEXIST';
  }
  return error;
}

export const windowsElevationScript = [
  "$ErrorActionPreference = 'Stop';",
  '$process = Start-Process',
  '-FilePath $env:WG_QUIC_ELEVATED_EXE',
  "-ArgumentList @('desktop-helper')",
  '-Verb RunAs',
  '-Wait',
  '-PassThru',
  '-WindowStyle Hidden;',
  'exit $process.ExitCode',
].join(' ');

export function windowsPowerShellPath(systemRoot?: string): string {
  const root = systemRoot || 'C:\\Windows';
  return path.win32.join(
    root,
    'System32',
    'WindowsPowerShell',
    'v1.0',
    'powershell.exe',
  );
}

export function windowsElevationEnvironment(
  executable: string,
  request: WindowsElevationRequest,
  resultPipe: string,
): NodeJS.ProcessEnv {
  return {
    WG_QUIC_ELEVATED_EXE: executable,
    WG_QUIC_DESKTOP_RESULT_PIPE: resultPipe,
    WG_QUIC_DESKTOP_ACTION: request.action,
    WG_QUIC_DESKTOP_NAME: request.name,
    WG_QUIC_DESKTOP_SOURCE: request.source || '',
    WG_QUIC_DESKTOP_OVERWRITE: request.overwrite ? '1' : '0',
  };
}

async function listenForWindowsElevationResult(timeout: number): Promise<{
  pipe: string;
  result: Promise<WindowsElevationResult>;
  close: () => Promise<void>;
}> {
  const pipe = `\\\\.\\pipe\\wg-quic-desktop-${process.pid}-${randomUUID()}`;
  const server = net.createServer();
  await new Promise<void>((resolve, reject) => {
    const onError = (error: Error) => reject(error);
    server.once('error', onError);
    server.listen(pipe, () => {
      server.off('error', onError);
      resolve();
    });
  });

  let activeSocket: net.Socket | undefined;
  let settled = false;
  let timer: NodeJS.Timeout;
  const result = new Promise<WindowsElevationResult>((resolve, reject) => {
    const succeed = (value: WindowsElevationResult): void => {
      if (!settled) {
        settled = true;
        clearTimeout(timer);
        resolve(value);
      }
    };
    const fail = (error: Error): void => {
      if (!settled) {
        settled = true;
        clearTimeout(timer);
        reject(error);
      }
    };
    timer = setTimeout(() => {
      fail(
        new Error(
          'The elevated wg-quic operation did not report a result.',
        ),
      );
    }, timeout);
    server.once('connection', (socket) => {
      activeSocket = socket;
      const chunks: Buffer[] = [];
      let size = 0;
      socket.on('data', (chunk: Buffer) => {
        size += chunk.length;
        if (size > 1024 * 1024) {
          socket.destroy(
            new Error('The elevated wg-quic result exceeded 1 MiB.'),
          );
          return;
        }
        chunks.push(chunk);
      });
      socket.once('error', fail);
      socket.once('end', () => {
        try {
          const parsed = JSON.parse(
            Buffer.concat(chunks).toString('utf8'),
          ) as Partial<WindowsElevationResult>;
          if (
            typeof parsed.success !== 'boolean' ||
            typeof parsed.message !== 'string'
          ) {
            throw new Error('The elevated wg-quic result was invalid.');
          }
          succeed({
            success: parsed.success,
            message: parsed.message,
          });
        } catch (error) {
          fail(error as Error);
        }
      });
    });
    server.once('error', fail);
  });
  result.catch(() => undefined);

  return {
    pipe,
    result,
    close: () =>
      new Promise<void>((resolve, reject) => {
        clearTimeout(timer);
        activeSocket?.destroy();
        if (!server.listening) {
          resolve();
          return;
        }
        server.close((error) => {
          if (error && !settled) {
            reject(error);
          } else {
            resolve();
          }
        });
      }),
  };
}

async function settleResult(
  result: Promise<WindowsElevationResult>,
  launcherError: string,
): Promise<WindowsElevationResult> {
  if (launcherError) {
    return Promise.race([
      result,
      new Promise<WindowsElevationResult>((_resolve, reject) => {
        setTimeout(() => reject(new Error(launcherError)), 1_000);
      }),
    ]);
  }
  try {
    return await result;
  } catch (error) {
    throw error;
  }
}

// runWindowsElevated keeps profile names and source paths out of the
// PowerShell program. The fixed helper subcommand reads a narrow request from
// its inherited environment after Windows has shown the UAC consent prompt.
export async function runWindowsElevated(
  executable: string,
  request: WindowsElevationRequest,
  timeout = 90_000,
): Promise<string> {
  const listener = await listenForWindowsElevationResult(timeout);
  const requestEnvironment = windowsElevationEnvironment(
    executable,
    request,
    listener.pipe,
  );
  let launcherError = '';

  try {
    try {
      await execFileAsync(
        windowsPowerShellPath(process.env.SystemRoot),
        [
          '-NoLogo',
          '-NoProfile',
          '-NonInteractive',
          '-ExecutionPolicy',
          'Bypass',
          '-Command',
          windowsElevationScript,
        ],
        {
          encoding: 'utf8',
          env: {
            ...process.env,
            ...requestEnvironment,
          },
          maxBuffer: 1024 * 1024,
          timeout,
          windowsHide: true,
        },
      );
    } catch (error) {
      const candidate = error as Error & {
        stderr?: string;
        stdout?: string;
      };
      launcherError =
        candidate.stderr?.trim() ||
        candidate.stdout?.trim() ||
        candidate.message;
    }

    const result = await settleResult(listener.result, launcherError);
    if (!result.success) {
      throw windowsElevationError(result.message, request);
    }
    if (launcherError) {
      throw windowsElevationError(launcherError, request);
    }
    return result.message;
  } finally {
    await listener.close();
  }
}
