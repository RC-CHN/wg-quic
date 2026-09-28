import { t } from "./i18n";
import type { TunnelAction, TunnelView } from "./types";

export interface ManagementServiceDisplay {
  label: string;
  state: "ready" | "fallback" | "error" | "hidden";
  needsAttention: boolean;
}

export function managementServiceDisplay(
  platform: string,
  status?: string,
): ManagementServiceDisplay {
  if (platform !== "win32") {
    return { label: "", state: "hidden", needsAttention: false };
  }
  switch (status) {
    case "ready":
      return {
        label: t("Management service ready"),
        state: "ready",
        needsAttention: false,
      };
    case "unauthorized":
      return {
        label: t("Administrator approval on changes"),
        state: "fallback",
        needsAttention: false,
      };
    case "incompatible":
      return {
        label: t("Management service needs an update"),
        state: "error",
        needsAttention: true,
      };
    case "unavailable":
      return {
        label: t("Management service unavailable"),
        state: "error",
        needsAttention: true,
      };
    default:
      return {
        label: t("Management service check failed"),
        state: "error",
        needsAttention: true,
      };
  }
}

export type TunnelDisplayState =
  | "active"
  | "connected"
  | "partial"
  | "connecting"
  | "authenticating"
  | "reconnecting"
  | "unknown"
  | "inactive"
  | "activating"
  | "deactivating";

export function tunnelDisplayState(
  tunnel: TunnelView,
  pending?: TunnelAction,
): TunnelDisplayState {
  if (pending === "up") {
    return "activating";
  }
  if (pending === "down") {
    return "deactivating";
  }
  if (tunnel.statusState === "unknown" || tunnel.statusDetail) return "unknown";
  if (tunnel.statusState === "prepared") return "activating";
  if (!tunnel.running) return "inactive";
  const peers = tunnel.status?.peers || [];
  const authenticated = new Set(
    (tunnel.status?.sessions || [])
      .filter((session) => session.state === "established")
      .flatMap((session) =>
        (session.peers || [])
          .filter((peer) => peer.authenticated)
          .map((peer) => peer.public_key),
      ),
  );
  const connected = peers.filter(
    (peer) =>
      peer.session === "established" && authenticated.has(peer.public_key),
  ).length;
  if (connected > 0)
    return connected === peers.length ? "connected" : "partial";
  if (peers.some((peer) => peer.session === "reconnecting"))
    return "reconnecting";
  if (peers.some((peer) => peer.session === "dialing")) return "connecting";
  if (peers.some((peer) => peer.session === "established"))
    return "authenticating";
  return "active";
}

export function tunnelStateLabel(state: TunnelDisplayState): string {
  switch (state) {
    case "active":
      return t("Waiting for peer");
    case "connected":
      return t("Connected");
    case "partial":
      return t("Partially connected");
    case "connecting":
      return t("Connecting…");
    case "authenticating":
      return t("Authenticating…");
    case "reconnecting":
      return t("Reconnecting…");
    case "unknown":
      return t("Status unavailable");
    case "inactive":
      return t("Inactive");
    case "activating":
      return t("Activating…");
    case "deactivating":
      return t("Deactivating…");
  }
}

export function actionProgressDescription(
  tunnel: TunnelView,
  action: TunnelAction,
  elapsedSeconds: number,
): string {
  const elapsed =
    elapsedSeconds >= 3 ? ` (${Math.floor(elapsedSeconds)}s)` : "";
  if (action === "up") {
    if (tunnel.running) {
      const sessions = tunnel.status?.stats.active_sessions || 0;
      return sessions > 0
        ? t("QUIC session established; finishing activation{0}", elapsed)
        : t("Interface is up; establishing QUIC session{0}", elapsed);
    }
    return t("Starting wg-quic-quick and creating the interface{0}", elapsed);
  }
  return tunnel.running
    ? t("Stopping the service and cleaning up host state{0}", elapsed)
    : t("Finishing deactivation{0}", elapsed);
}

export function chooseSelectedTunnel(
  tunnels: TunnelView[],
  selectedName?: string,
): string | undefined {
  if (
    selectedName &&
    tunnels.some((candidate) => candidate.name === selectedName)
  ) {
    return selectedName;
  }
  return tunnels[0]?.name;
}

export function formatBytes(value = 0): string {
  if (!Number.isFinite(value) || value <= 0) {
    return "0 B";
  }
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  const exponent = Math.min(
    Math.floor(Math.log(value) / Math.log(1024)),
    units.length - 1,
  );
  const scaled = value / 1024 ** exponent;
  return `${scaled >= 100 || exponent === 0 ? scaled.toFixed(0) : scaled.toFixed(1)} ${units[exponent]}`;
}

export function formatBitRate(value = 0): string {
  if (!Number.isFinite(value) || value <= 0) {
    return "—";
  }
  const units = ["bps", "Kbps", "Mbps", "Gbps", "Tbps"];
  const exponent = Math.min(
    Math.floor(Math.log(value) / Math.log(1000)),
    units.length - 1,
  );
  const scaled = value / 1000 ** exponent;
  return `${scaled >= 100 || exponent === 0 ? scaled.toFixed(0) : scaled.toFixed(1)} ${units[exponent]}`;
}

export function formatRTT(microseconds = 0): string {
  return microseconds > 0
    ? `${(microseconds / 1000).toFixed(microseconds < 10_000 ? 1 : 0)} ms`
    : "—";
}

export function formatHandshakeAge(
  timestamp?: number,
  now = Date.now(),
): string {
  if (
    !timestamp ||
    !Number.isFinite(new Date(timestamp * 1000).getTime()) ||
    timestamp < 0
  )
    return t("No handshake yet");
  const seconds = Math.floor(now / 1000 - timestamp);
  if (seconds < -5) return t("Check system clock");
  if (seconds < 5) return t("Just now");
  if (seconds < 60) return t("{0} s ago", seconds);
  if (seconds < 3600) return t("{0} min ago", Math.floor(seconds / 60));
  if (seconds < 86400) return t("{0} h ago", Math.floor(seconds / 3600));
  return t("{0} d ago", Math.floor(seconds / 86400));
}

export function formatFECRecovery(recovered = 0, rawLost = 0): string {
  if (rawLost <= 0) {
    return t("No observed loss");
  }
  return t("{0}% recovered", ((recovered / rawLost) * 100).toFixed(1));
}

export function managementErrorMessage(message: string): string {
  if (/outcome is unknown/i.test(message)) {
    return t(
      "{0} Tunnel status was refreshed; verify it before retrying.",
      message,
    );
  }
  if (/administrator privileges|administrator approval/i.test(message)) {
    return message;
  }
  if (
    /access is denied|permission denied|requested operation requires elevation|privilege is not held/i.test(
      message,
    )
  ) {
    return t("{0} Administrator privileges may be required.", message);
  }
  return message;
}

export function createSingleFlight<T>(
  operation: () => Promise<T>,
): () => Promise<T> {
  let inFlight: Promise<T> | undefined;
  return () => {
    if (inFlight) {
      return inFlight;
    }
    const current = operation().finally(() => {
      if (inFlight === current) {
        inFlight = undefined;
      }
    });
    inFlight = current;
    return current;
  };
}
