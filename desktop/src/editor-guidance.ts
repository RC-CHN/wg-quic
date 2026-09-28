// Field IDs are shared by inline errors and the keyboard-accessible summary.
export const errorFields: Record<string, string> = {
  "Tunnel name is required.": "form-name",
  "Private key must be a base64 WireGuard key.": "form-private-key",
  "Tunnel address must be a CIDR like 10.0.0.2/32.": "form-addresses",
  "Listen port must be between 1 and 65535.": "form-listen-port",
  "MTU must be between 576 and 65535.": "form-mtu",
  "Peer public key must be a base64 WireGuard key.": "form-peer-public-key",
  "Preshared key must be a base64 WireGuard key.": "form-preshared-key",
  "Allowed IPs must be CIDRs like 0.0.0.0/0.": "form-allowed-ips",
  "Keepalive must be 0-65535 seconds or off.": "form-keepalive",
  "Endpoint must include a host and a port from 1 to 65535.": "form-endpoint",
};

export function routesAllTraffic(value: string): boolean {
  const prefixes = new Set(value.split(",").map((part) => part.trim()));
  return prefixes.has("0.0.0.0/0") && prefixes.has("::/0");
}
