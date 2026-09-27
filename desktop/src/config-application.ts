import type { ApplyResult } from './types';

export type PendingConfiguration = Omit<ApplyResult, 'state'> & { state: ApplyResult['state'] | 'saved' };

// Persist only the pending flag and opaque transaction ID, never configuration
// text, keys or diagnostic messages. An external CLI change is not inferred to
// have succeeded: Apply verifies it through the authoritative supervisor.
export class ConfigurationApplications {
  private readonly values = new Map<string, PendingConfiguration>();
  constructor(private readonly storage: Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>) {}
  get(path: string): PendingConfiguration | undefined {
    if (this.values.has(path)) return this.values.get(path);
    try {
      const value = JSON.parse(this.storage.getItem(`wg-quic-pending:${path}`) || 'null');
      if (value && ['saved', 'restart_required', 'failed', 'unknown'].includes(value.state)) {
        const result: PendingConfiguration = { state: value.state };
        if (typeof value.request_id === 'string' && /^[0-9a-f]{32}$/i.test(value.request_id)) result.request_id = value.request_id;
        this.values.set(path, result);
        return result;
      }
    } catch { /* Restricted or corrupted browser storage does not block control. */ }
    return undefined;
  }
  set(path: string, value: PendingConfiguration): void {
    if (value.state === 'applied') { this.clear(path); return; }
    this.values.set(path, value);
    try { this.storage.setItem(`wg-quic-pending:${path}`, JSON.stringify({ state: value.state, request_id: value.request_id })); } catch { /* Keep the in-memory state. */ }
  }
  clear(path: string): void {
    this.values.delete(path);
    try { this.storage.removeItem(`wg-quic-pending:${path}`); } catch { /* Best effort. */ }
  }
}
