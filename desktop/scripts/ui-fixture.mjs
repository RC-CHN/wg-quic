// Synthetic data for browser layout checks and narrated UI recordings only.
// Never imported by the shipped application and never opens a real tunnel.
export function installUIFixture() {
  const publicKey = "AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";
  const privateKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";
  const configuration = `# wg-quic: congestion = auto\n# wg-quic: fec = auto\n# wg-quic: obfs = salamander\n[Interface]\nPrivateKey = ${privateKey}\nAddress = 10.24.0.2/32\n[Peer]\nPublicKey = ${publicKey}\nAllowedIPs = 10.24.0.0/16\nEndpoint = vpn.example.com:51820\n`;
  const status = (name) => ({
    interface: name,
    observation_id: `demo-${name}-${Date.now()}`,
    state: "up",
    listen_port: 51820,
    carrier: "quic",
    fec_mode: "auto",
    obfs_mode: "salamander",
    public_key: publicKey,
    addresses: ["10.24.0.2/32"],
    stats: {
      wg_rx_bytes: 12582912,
      wg_tx_bytes: 3145728,
      quic_smoothed_rtt_us: 24600,
      active_sessions: 1,
      quic_bandwidth_estimate_bps: 42000000,
      quic_pacing_rate_bps: 45000000,
      fec_recovered: 18,
      fec_raw_lost: 20,
      fec_unrecovered: 2,
      fec_current_parity_shards: 2,
    },
    peers: [
      {
        public_key: publicKey,
        generation: 1,
        endpoint: "vpn.example.com:51820",
        session: "established",
        latest_handshake: Math.floor(Date.now() / 1000),
        transfer_rx: 12582912,
        transfer_tx: 3145728,
      },
    ],
    sessions: [
      {
        session_id: 1,
        state: "established",
        peers: [{ public_key: publicKey, authenticated: true }],
      },
    ],
    recent_sessions: [
      {
        session_id: 0,
        closed_at: new Date().toISOString(),
        close_reason: "idle_timeout",
        current_endpoint: "vpn.example.com:51820",
      },
    ],
  });
  const tunnel = (name, running) => ({
    name,
    configPath: `/etc/wg-quic/${name}.conf`,
    running,
    sampledAt: Date.now(),
    statusState: running ? "up" : "inactive",
    ...(running ? { status: status(name) } : {}),
  });
  const state = {
    snapshot: {
      backend: {
        supported: true,
        platform: "linux",
        arch: "x64",
        configDirectory: "/etc/wg-quic",
        coreVersion: "review",
        quickVersion: "review",
      },
      refreshedAt: String(Date.now()),
      tunnels: [tunnel("home", true), tunnel("office", false)],
    },
    configurations: { home: configuration, office: configuration },
    calls: [],
    failSave: false,
  };
  const getSnapshot = () => {
    const now = Date.now();
    for (const item of state.snapshot.tunnels) {
      if (!item.running || !item.status) continue;
      const seconds = Math.max(0, now - item.sampledAt) / 1000;
      const rx = Math.round(seconds * 256000),
        tx = Math.round(seconds * 64000);
      item.status.stats.wg_rx_bytes += rx;
      item.status.stats.wg_tx_bytes += tx;
      item.status.peers[0].transfer_rx += rx;
      item.status.peers[0].transfer_tx += tx;
      item.sampledAt = now;
    }
    state.snapshot.refreshedAt = String(Date.now());
    return structuredClone(state.snapshot);
  };
  window.__uiReview = state;
  window.__TAURI_INTERNALS__ = {
    invoke: async (command, args = {}) => {
      state.calls.push(command);
      if (command === "desktop_smoke_settings") return { mode: "tray" };
      if (command === "report_desktop_smoke") return;
      if (command === "snapshot") return getSnapshot();
      if (command === "generate_keys")
        return JSON.stringify({
          private_key: privateKey,
          public_key: publicKey,
        });
      if (command === "derive_public_key") return publicKey;
      if (command === "read_tunnel")
        return state.configurations[args.name] || configuration;
      if (command === "check_tunnel") return "Configuration is valid.";
      if (command === "write_tunnel") {
        await new Promise((resolve) => setTimeout(resolve, 650));
        if (state.failSave) {
          state.failSave = false;
          throw Error(
            document.documentElement.lang.startsWith("zh")
              ? "演示：配置暂时无法保存，草稿已保留，请重试。"
              : "Demo: configuration could not be saved. Your draft is kept; try again.",
          );
        }
        state.configurations[args.name] = args.contents;
        if (!state.snapshot.tunnels.some((item) => item.name === args.name))
          state.snapshot.tunnels.push(tunnel(args.name, false));
        return getSnapshot();
      }
      if (command === "manage_tunnel") {
        const index = state.snapshot.tunnels.findIndex(
          (item) => item.name === args.name,
        );
        state.snapshot.tunnels[index] = tunnel(args.name, args.action === "up");
        if (args.action === "up") {
          state.snapshot.tunnels[index].status.peers[0].session = "dialing";
          state.snapshot.tunnels[index].status.sessions = [];
        }
        await new Promise((resolve) => setTimeout(resolve, 1600));
        state.snapshot.tunnels[index] = tunnel(args.name, args.action === "up");
        return getSnapshot();
      }
      if (command === "apply_tunnel") {
        await new Promise((resolve) => setTimeout(resolve, 700));
        return { state: "applied" };
      }
      if (command === "plugin:dialog|open") return "/demo/imported.conf";
      if (command === "import_config") {
        if (!state.snapshot.tunnels.some((item) => item.name === "imported"))
          state.snapshot.tunnels.push(tunnel("imported", false));
        return {
          canceled: false,
          importedName: "imported",
          snapshot: getSnapshot(),
        };
      }
      if (command.startsWith("plugin:dialog|")) return true;
      throw Error(`Unexpected UI review operation: ${command}`);
    },
  };
}
