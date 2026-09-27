import type { TunnelView } from './types';

export interface PeerRate { rx?: number; tx?: number }
interface Sample { generation: number; rx?: number; tx?: number }
interface Observation { epoch: string; at: number; peers: Map<string, Sample>; rates: Map<string, PeerRate> }

export class PeerRates {
  private observations = new Map<string, Observation>();
  observe(tunnel: TunnelView): Map<string, PeerRate> {
    const epoch = tunnel.status?.observation_id;
    const at = tunnel.sampledAt;
    if (!tunnel.running || tunnel.statusState === 'unknown' || !epoch || !at) {
      this.observations.delete(tunnel.configPath);
      return new Map();
    }
    const previous = this.observations.get(tunnel.configPath);
    if (previous?.epoch === epoch && previous.at === at) return previous.rates;
    const seconds = previous ? (at - previous.at) / 1000 : 0;
    const rates = new Map<string, PeerRate>();
    const peers = new Map<string, Sample>();
    const delta = (now?: number, before?: number): number | undefined =>
      Number.isSafeInteger(now) && Number.isSafeInteger(before) && now! >= before! ? (now! - before!) * 8 / seconds : undefined;
    for (const peer of tunnel.status?.peers || []) {
      const sample = { generation: peer.generation, rx: peer.transfer_rx, tx: peer.transfer_tx };
      peers.set(peer.public_key, sample);
      const before = previous?.peers.get(peer.public_key);
      if (previous?.epoch === epoch && seconds > 0 && seconds <= 15 && before?.generation === peer.generation) {
        rates.set(peer.public_key, { rx: delta(sample.rx, before.rx), tx: delta(sample.tx, before.tx) });
      }
    }
    if (this.observations.size >= 256) this.observations.delete(this.observations.keys().next().value!);
    this.observations.set(tunnel.configPath, { epoch, at, peers, rates });
    return rates;
  }
}
