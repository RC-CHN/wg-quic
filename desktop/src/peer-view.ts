import { t, currentLanguage } from "./i18n";
import type { TunnelView } from "./types";
import { PeerRates } from "./peer-rates";
import { formatBitRate, formatBytes, formatHandshakeAge } from "./view-model";
import { copyText } from "./clipboard";

const rates = new PeerRates();
export function renderPeers(
  tunnel: TunnelView,
  notify: (text: string, kind?: "ok" | "error") => void,
): void {
  const list = document.getElementById("peer-list")!;
  const status = tunnel.status;
  const observations = rates.observe(tunnel);
  const existing = new Map(
    Array.from(list.querySelectorAll<HTMLElement>("[data-peer]")).map((row) => [
      row.dataset.peer!,
      row,
    ]),
  );
  list.querySelector(".peer-empty")?.remove();
  const peers = status?.peers || [];
  for (const [index, peer] of peers.entries()) {
    let row = existing.get(peer.public_key);
    existing.delete(peer.public_key);
    if (!row) {
      row = document.createElement("article");
      row.className = "peer-row";
      row.dataset.peer = peer.public_key;
      // Only fixed markup goes into this template; all peer data is textContent.
      row.innerHTML = `
        <div class="peer-heading">
          <div class="peer-identity">
            <h3 data-field="endpoint"></h3>
            <div class="peer-key-line"><span data-label="Public key"></span><code data-field="key"></code>
              <button type="button" class="copy-key"><svg viewBox="0 0 20 20" aria-hidden="true"><rect x="7" y="7" width="9" height="10" rx="2"/><path d="M12 7V4a1 1 0 0 0-1-1H4a1 1 0 0 0-1 1v7a1 1 0 0 0 1 1h3"/></svg><span data-label="Copy"></span></button>
            </div>
          </div><span data-field="state" class="peer-state"></span>
        </div>
        <div class="peer-transfer">
          <div class="peer-traffic"><span data-label="Receive rate" class="traffic-label"></span><strong data-field="rx-rate" class="peer-rate"></strong><div class="peer-total"><span data-label="Received total"></span><span data-field="rx-total"></span></div></div>
          <div class="peer-traffic"><span data-label="Send rate" class="traffic-label"></span><strong data-field="tx-rate" class="peer-rate"></strong><div class="peer-total"><span data-label="Sent total"></span><span data-field="tx-total"></span></div></div>
        </div>
        <div class="peer-handshake"><span data-label="Last handshake"></span><time data-field="handshake"></time><span data-field="retry" class="peer-retry hidden"></span></div>`;
      row.querySelector("button")!.addEventListener(
        "click",
        () =>
          void copyText(peer.public_key).then(
            () => notify(t("Public key copied")),
            (error) => notify(String(error), "error"),
          ),
      );
    }
    const field = (name: string) =>
      row!.querySelector<HTMLElement>(`[data-field="${name}"]`)!;
    const text = (name: string, value: string) => {
      const target = field(name);
      if (target.textContent !== value) target.textContent = value;
    };
    row.querySelectorAll<HTMLElement>("[data-label]").forEach((label) => {
      label.textContent = t(label.dataset.label!);
    });
    const known = tunnel.statusState !== "unknown";
    const authenticated =
      known &&
      status?.sessions?.some(
        (session) =>
          session.state === "established" &&
          session.peers?.some(
            (association) =>
              association.public_key === peer.public_key &&
              association.authenticated,
          ),
      );
    field("state").className =
      `peer-state ${authenticated ? "ready" : known ? "pending" : "unknown"}`;
    text(
      "state",
      !known
        ? t("Status unavailable")
        : authenticated
          ? t("Connected")
          : peer.session === "established"
            ? t("Authenticating…")
            : peer.session === "reconnecting"
              ? t("Reconnecting…")
              : peer.session === "dialing"
                ? t("Connecting…")
                : t("Waiting"),
    );
    text("endpoint", peer.endpoint || t("Endpoint pending"));
    text(
      "key",
      peer.public_key.length > 24
        ? `${peer.public_key.slice(0, 10)}…${peer.public_key.slice(-8)}`
        : peer.public_key,
    );
    field("key").title = peer.public_key;
    row
      .querySelector("button")!
      .setAttribute("aria-label", t("Copy public key"));
    row.querySelector("button")!.title = t("Copy public key");
    const rate = observations.get(peer.public_key);
    for (const direction of ["rx", "tx"] as const) {
      const value = rate?.[direction];
      const formatted = !known
        ? "—"
        : value === undefined
          ? t("Measuring…")
          : value === 0
            ? "0 bps"
            : formatBitRate(value);
      const target = field(`${direction}-rate`);
      if (target.textContent !== formatted) {
        const split = formatted.lastIndexOf(" ");
        if (value !== undefined && known && split > 0) {
          const unit = document.createElement("span");
          unit.className = "metric-unit";
          unit.textContent = formatted.slice(split);
          target.replaceChildren(
            document.createTextNode(formatted.slice(0, split)),
            unit,
          );
        } else target.textContent = formatted;
      }
      target.classList.toggle("unavailable", !known || value === undefined);
      const total = direction === "rx" ? peer.transfer_rx : peer.transfer_tx;
      text(
        `${direction}-total`,
        known && total !== undefined ? formatBytes(total) : "—",
      );
    }
    text(
      "handshake",
      known
        ? formatHandshakeAge(peer.latest_handshake)
        : t("Status unavailable"),
    );
    const handshake = field("handshake") as HTMLTimeElement;
    if (
      peer.latest_handshake &&
      Number.isFinite(new Date(peer.latest_handshake * 1000).getTime())
    ) {
      const date = new Date(peer.latest_handshake * 1000);
      handshake.dateTime = date.toISOString();
      handshake.title = date.toLocaleString(
        currentLanguage() === "zh" ? "zh-CN" : "en-US",
        { hour12: false },
      );
    } else {
      handshake.removeAttribute("datetime");
      handshake.removeAttribute("title");
    }
    const retrying =
      known && peer.session === "reconnecting" && Boolean(peer.next_reconnect);
    field("retry").classList.toggle("hidden", !retrying);
    text(
      "retry",
      retrying
        ? t(
            "Retry in {0} s · {1} attempts",
            Math.max(0, Math.ceil(peer.next_reconnect! - Date.now() / 1000)),
            peer.reconnect_attempts || 0,
          )
        : "",
    );
    if (list.children[index] !== row)
      list.insertBefore(row, list.children[index] || null);
  }
  for (const row of existing.values()) row.remove();
  if (!peers.length) {
    const empty = document.createElement("div");
    empty.className = "peer-empty";
    empty.textContent = status
      ? t("No peers reported by the running interface.")
      : t("Peer status appears when the tunnel is active.");
    list.replaceChildren(empty);
  }
  const history = document.getElementById("session-history-list")!;
  const reasons: Record<string, string> = {
    authentication_timeout: t(
      "WireGuard authentication timed out. Check both public keys and the preshared key.",
    ),
    idle_timeout: t(
      "The peer stopped responding. Check connectivity, firewall rules and the peer service.",
    ),
    remote_close: t("The remote peer closed the connection."),
    local_shutdown: t("The local tunnel was stopped."),
    endpoint_replaced: t("A replacement connection took over."),
    configuration_removed: t("The peer was removed from the configuration."),
    handshake_timeout: t(
      "QUIC handshake timed out. Check the endpoint and UDP connectivity.",
    ),
  };
  const sessions = [...(status?.recent_sessions || [])].slice(-8).reverse();
  history.replaceChildren(
    ...sessions.map((session) => {
      const row = document.createElement("p");
      row.textContent = `${new Date(session.closed_at).toLocaleTimeString(currentLanguage() === "zh" ? "zh-CN" : "en-US", { hour12: false })} · ${session.current_endpoint || ""} · ${reasons[session.close_reason] || session.close_reason}`;
      if (session.last_error) row.title = session.last_error;
      return row;
    }),
  );
  if (!sessions.length)
    history.textContent = t("No recent disconnections reported.");
}
