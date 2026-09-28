//! A bounded, one-peer interoperability probe, not a production VPN.
//! Protocol implementation is derived solely from WG-QUIC-PROTOCOL.md.
mod crypto;
mod wire;
use anyhow::{Context as _, Result, ensure};
use crypto::{Key, Noise};
use quinn::{
    AsyncUdpSocket, Endpoint, UdpPoller,
    crypto::rustls::{QuicClientConfig, QuicServerConfig},
    udp::{RecvMeta, Transmit},
};
use rustls::pki_types::{CertificateDer, PrivatePkcs8KeyDer, ServerName, UnixTime};
use serde_json::{Value, json};
use std::{
    io::{self, IoSliceMut},
    net::SocketAddr,
    pin::Pin,
    sync::Arc,
    task::{Context, Poll},
    time::{Duration, SystemTime, UNIX_EPOCH},
};
use tokio::net::UdpSocket;

// The adapter deliberately advertises no UDP batching; each QUIC UDP payload
// has its own salt/hint. Standard Quinn supplies all QUIC/TLS wire behavior.
#[derive(Debug)]
struct Socket {
    udp: UdpSocket,
    key: Option<Key>,
}
#[derive(Debug)]
struct Poller(Arc<Socket>);
impl UdpPoller for Poller {
    fn poll_writable(self: Pin<&mut Self>, cx: &mut Context) -> Poll<io::Result<()>> {
        self.0.udp.poll_send_ready(cx)
    }
}
impl AsyncUdpSocket for Socket {
    fn create_io_poller(self: Arc<Self>) -> Pin<Box<dyn UdpPoller>> {
        Box::pin(Poller(self))
    }
    fn try_send(&self, t: &Transmit) -> io::Result<()> {
        let encoded;
        let bytes = if let Some(key) = self.key {
            encoded = crypto::envelope(&key, &crypto::random(), t.contents);
            encoded.as_slice()
        } else {
            t.contents
        };
        self.udp.try_send_to(bytes, t.destination).map(|_| ())
    }
    fn poll_recv(
        &self,
        cx: &mut Context,
        bufs: &mut [IoSliceMut<'_>],
        meta: &mut [RecvMeta],
    ) -> Poll<io::Result<usize>> {
        for _ in 0..32 {
            let mut buffer = [0u8; 65535];
            let mut rb = tokio::io::ReadBuf::new(&mut buffer);
            let remote = match self.udp.poll_recv_from(cx, &mut rb) {
                Poll::Pending => return Poll::Pending,
                Poll::Ready(Err(e)) => return Poll::Ready(Err(e)),
                Poll::Ready(Ok(addr)) => addr,
            };
            let plain = match self.key {
                Some(key) => match crypto::unwrap(&key, rb.filled()) {
                    Ok(p) => p,
                    Err(_) => continue,
                },
                None => rb.filled().to_vec(),
            };
            if plain.len() > bufs[0].len() {
                continue;
            }
            bufs[0][..plain.len()].copy_from_slice(&plain);
            meta[0] = RecvMeta {
                addr: remote,
                len: plain.len(),
                stride: plain.len(),
                ecn: None,
                dst_ip: None,
            };
            return Poll::Ready(Ok(1));
        }
        cx.waker().wake_by_ref();
        Poll::Pending
    }
    fn local_addr(&self) -> io::Result<SocketAddr> {
        self.udp.local_addr()
    }
}

// Outer TLS verifies possession of the certificate key, but does not bind it
// to peer identity. Inner WireGuard authenticates the configured static key.
#[derive(Debug)]
struct CertificatePolicy(Arc<rustls::crypto::CryptoProvider>);
impl rustls::client::danger::ServerCertVerifier for CertificatePolicy {
    fn verify_server_cert(
        &self,
        _: &CertificateDer<'_>,
        _: &[CertificateDer<'_>],
        _: &ServerName<'_>,
        _: &[u8],
        _: UnixTime,
    ) -> Result<rustls::client::danger::ServerCertVerified, rustls::Error> {
        Ok(rustls::client::danger::ServerCertVerified::assertion())
    }
    fn verify_tls12_signature(
        &self,
        m: &[u8],
        c: &CertificateDer<'_>,
        s: &rustls::DigitallySignedStruct,
    ) -> Result<rustls::client::danger::HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls12_signature(m, c, s, &self.0.signature_verification_algorithms)
    }
    fn verify_tls13_signature(
        &self,
        m: &[u8],
        c: &CertificateDer<'_>,
        s: &rustls::DigitallySignedStruct,
    ) -> Result<rustls::client::danger::HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls13_signature(m, c, s, &self.0.signature_verification_algorithms)
    }
    fn supported_verify_schemes(&self) -> Vec<rustls::SignatureScheme> {
        self.0.signature_verification_algorithms.supported_schemes()
    }
}
fn key(v: &Value, name: &str) -> Result<Key> {
    Ok(hex::decode(v[name].as_str().context("missing key")?)?
        .as_slice()
        .try_into()?)
}
fn timestamp() -> [u8; 12] {
    let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap();
    let mut t = [0; 12];
    t[..8].copy_from_slice(&(0x400000000000000a + now.as_secs()).to_be_bytes());
    t[8..].copy_from_slice(&now.subsec_nanos().to_be_bytes());
    t
}
fn inner_packet() -> Vec<u8> {
    let payload = b"rust-to-go";
    let mut p = vec![0; 20 + payload.len()];
    p[0] = 0x45;
    p[2..4].copy_from_slice(&((20 + payload.len()) as u16).to_be_bytes());
    p[8] = 64;
    p[9] = 253;
    p[12..16].copy_from_slice(&[10, 200, 0, 2]);
    p[16..20].copy_from_slice(&[10, 200, 0, 1]);
    p[20..].copy_from_slice(payload);
    let mut sum: u32 = p[..20]
        .chunks_exact(2)
        .map(|b| u16::from_be_bytes([b[0], b[1]]) as u32)
        .sum();
    while sum > 65535 {
        sum = (sum & 65535) + (sum >> 16)
    }
    p[10..12].copy_from_slice(&(!(sum as u16)).to_be_bytes());
    p
}
fn check_inner(p: &[u8]) -> Result<Vec<u8>> {
    ensure!(p.len() >= 20 && p[0] >> 4 == 4, "expected IPv4");
    let size = u16::from_be_bytes(p[2..4].try_into()?) as usize;
    ensure!(
        size >= 20
            && size <= p.len()
            && p[12..16] == [10, 200, 0, 1]
            && p[16..20] == [10, 200, 0, 2],
        "inner length or AllowedIPs"
    );
    Ok(p[..size].to_vec())
}
fn send(c: &quinn::Connection, id: &mut u64, p: &[u8]) -> Result<()> {
    let budget = c.max_datagram_size().context("DATAGRAM not negotiated")?;
    let size = std::cmp::min(64, budget.checked_sub(21).context("DATAGRAM too small")?);
    ensure!(size > 0, "DATAGRAM too small");
    let count = p.len().div_ceil(size);
    ensure!(count <= 128, "too many fragments");
    for (i, part) in p.chunks(size).enumerate() {
        c.send_datagram(wire::frame(*id, i as u16, count as u16, p.len() as u32, part).into())?
    }
    *id += 1;
    Ok(())
}

#[tokio::main]
async fn main() -> Result<()> {
    let path = std::env::args()
        .nth(1)
        .context("usage: wg-quic-independent-interop CONFIG.json")?;
    let config: Value = serde_json::from_slice(&std::fs::read(path)?)?;
    let secret = key(&config, "private")?;
    let peer = key(&config, "peer")?;
    let psk = if config["psk"].is_string() {
        Some(key(&config, "psk")?)
    } else {
        None
    };
    let listen: SocketAddr = config["listen"].as_str().unwrap_or("127.0.0.1:0").parse()?;
    ensure!(
        listen.ip().is_loopback(),
        "this test probe only binds loopback"
    );
    let obfs = match config["obfs"].as_str() {
        Some("salamander") => Some(crypto::salamander_key(&secret, &peer, psk.as_ref())?),
        Some("none") => None,
        _ => anyhow::bail!("obfs mode"),
    };
    let socket = Arc::new(Socket {
        udp: UdpSocket::bind(listen).await?,
        key: obfs,
    });
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let cert = rcgen::generate_simple_self_signed(vec!["localhost".into()])?;
    let mut tls = rustls::ServerConfig::builder_with_provider(provider.clone())
        .with_protocol_versions(&[&rustls::version::TLS13])?
        .with_no_client_auth()
        .with_single_cert(
            vec![cert.cert.der().clone()],
            PrivatePkcs8KeyDer::from(cert.signing_key.serialize_der()).into(),
        )?;
    tls.alpn_protocols = vec![b"wg-quic/1".to_vec()];
    let mut transport = quinn::TransportConfig::default();
    transport
        .max_concurrent_bidi_streams(0u32.into())
        .max_concurrent_uni_streams(0u32.into())
        .datagram_receive_buffer_size(Some(1 << 20))
        .keep_alive_interval(Some(Duration::from_secs(5)))
        .max_idle_timeout(Some(Duration::from_secs(15).try_into()?));
    let transport = Arc::new(transport);
    let mut server = quinn::ServerConfig::with_crypto(Arc::new(QuicServerConfig::try_from(tls)?));
    server.transport_config(transport.clone());
    let mut endpoint = Endpoint::new_with_abstract_socket(
        quinn::EndpointConfig::default(),
        Some(server),
        socket,
        Arc::new(quinn::TokioRuntime),
    )?;
    let mut client_tls = rustls::ClientConfig::builder_with_provider(provider.clone())
        .with_protocol_versions(&[&rustls::version::TLS13])?
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(CertificatePolicy(provider)))
        .with_no_client_auth();
    client_tls.alpn_protocols = vec![b"wg-quic/1".to_vec()];
    let mut client = quinn::ClientConfig::new(Arc::new(QuicClientConfig::try_from(client_tls)?));
    client.transport_config(transport);
    endpoint.set_default_client_config(client);
    println!(
        "{}",
        json!({"event":"ready","port":endpoint.local_addr()?.port()})
    );
    let initiator = config["initiator"].as_bool().unwrap_or(false);
    let connection = tokio::time::timeout(Duration::from_secs(10), async {
        if initiator {
            let remote: SocketAddr = config["remote"].as_str().context("remote")?.parse()?;
            ensure!(remote.ip().is_loopback(), "loopback remote only");
            Ok(endpoint.connect(remote, "localhost")?.await?)
        } else {
            Ok::<_, anyhow::Error>(endpoint.accept().await.context("listener closed")?.await?)
        }
    })
    .await??;
    ensure!(
        connection.max_datagram_size().is_some(),
        "DATAGRAM not negotiated"
    );
    println!(
        "{}",
        json!({"event":"quic","role":if initiator{"client"}else{"server"}})
    );
    let mut noise = Noise::new(secret, peer, psk.unwrap_or([0; 32]));
    let mut decoder = wire::Decoder::default();
    decoder.drop_first_data = config["drop_first_data"].as_bool().unwrap_or(false);
    let mut packet_id = 1;
    if initiator {
        send(
            &connection,
            &mut packet_id,
            &noise.initiate(crypto::random(), timestamp())?,
        )?
    }
    let mut confirmed = false;
    let mut transmitted = false;
    let mut received = 0;
    let start = tokio::time::Instant::now();
    let mut timer = tokio::time::interval(Duration::from_millis(100));
    let mut next_retry = Duration::from_secs(5);
    let mut done_at = None;
    loop {
        ensure!(
            start.elapsed() < Duration::from_secs(25),
            "interop timed out"
        );
        tokio::select! {
            _ = timer.tick() => {
                for reply in decoder.expire() {
                    connection.send_datagram(reply.into())?;
                }
                if initiator && !confirmed && start.elapsed() >= next_retry {
                    send(&connection, &mut packet_id, &noise.initiate(crypto::random(), timestamp())?)?;
                    next_retry += Duration::from_secs(5);
                }
                if done_at.is_some_and(|t: tokio::time::Instant| t.elapsed() > Duration::from_millis(400)) {
                    break;
                }
            }
            data = connection.read_datagram() => {
                let data = data?;
                let (packets, replies) = decoder.feed(&data)?;
                for reply in replies {
                    connection.send_datagram(reply.into())?;
                }
                for p in packets {
                    ensure!(p.len() >= 4, "short WireGuard message");
                    match u32::from_le_bytes(p[..4].try_into()?) {
                        1 => {
                            let response = noise.respond(&p, crypto::random())?;
                            send(&connection, &mut packet_id, &response)?;
                        }
                        2 => {
                            noise.consume_response(&p)?;
                            send(&connection, &mut packet_id, &noise.transport(&[])?)?;
                            confirmed = true;
                        }
                        3 => {
                            noise.cookie(&p)?;
                            send(&connection, &mut packet_id, &noise.initiate(crypto::random(), timestamp())?)?;
                        }
                        4 => {
                            let plain = noise.decrypt(&p)?;
                            confirmed = true;
                            if !plain.is_empty() {
                                let ip = check_inner(&plain)?;
                                ensure!(ip[20..].starts_with(b"go-to-rust"), "payload mismatch");
                                received += 1;
                            }
                        }
                        _ => anyhow::bail!("unknown WireGuard type"),
                    }
                    if confirmed && !transmitted {
                        send(&connection, &mut packet_id, &noise.transport(&inner_packet())?)?;
                        transmitted = true;
                    }
                    if confirmed && transmitted && received > 0 && done_at.is_none() {
                        done_at = Some(tokio::time::Instant::now());
                    }
                }
            }
        }
    }

    println!(
        "{}",
        json!({"event":"passed","inner_received":received,"inner_sent":u32::from(transmitted),"fec_data":decoder.data_count,"fec_parity":decoder.parity_count,"fec_recovered":decoder.recovered})
    );
    endpoint.close(0u32.into(), b"probe complete");
    Ok(())
}

#[cfg(test)]
mod tests;
