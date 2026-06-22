# Datasheet: `relay/transport`

Connects the media transport to one relay endpoint: a UDP socket, a DTLS client
handshake, an SCTP association, and a pre-negotiated id=0 WebRTC DataChannel that
carries STUN/RTP/RTCP as binary messages. Transport layer; this is the wire under
the media plane.

**Validation vector:** (integration — no single vector). The only directly
testable unit is the first-byte packet classifier, pinned by the inline assertions
in the `tests` module below. The connection path (`connect_relay_media`) talks to a
live relay and has no recorded vector. If a classifier vector file is later
extracted, copy it verbatim into `relay/testdata/`.

**Reference pinned at:** `41095d4e6ba4610e054e9ede3af1d5e88a83faee` (whatsapp-rust
`src/voip/transport.rs` — note the **main crate** `src/`, not `wacore/src/`; the
earlier `wacore/`-only search mis-flagged this UNMAPPED). Only `classify_relay_packet`
is pure/unit-testable (pinned by the inline `tests` below); the connection path
(`connect_relay_media`) binds a webrtc-rs DTLS/SCTP/DataChannel stack and has **no
vector** — it requires a human decision on the Go transport library and the
dependency policy before it can be built.

## Reference source (verbatim — authoritative)

```rust
//! Relay media transport: a pre-negotiated WebRTC DataChannel over SCTP-over-DTLS-over-UDP
//! to a single WhatsApp relay endpoint. The synthetic-SDP / wrtc dance reduces, at this layer,
//! to: connect a UDP socket to the relay, DTLS-handshake as the client (self-signed cert,
//! server-cert verification skipped, since SRTP keys come from callKey/hbh_key, not DTLS),
//! run an SCTP association over it, and open the pre-negotiated id=0 DataChannel that carries
//! STUN/RTP/RTCP as binary messages.
//!
//! NOTE: live validation against a real relay is deferred (see docs/voip/PORT_PLAN.md). This
//! wires the webrtc-rs stack and compiles; `connect_relay_media` is not exercised in CI.

use std::net::SocketAddr;
use std::sync::Arc;

use anyhow::{Context, Result, anyhow};
use async_trait::async_trait;
use bytes::Bytes;
use tokio::net::UdpSocket;
use webrtc_data::data_channel::{Config as DcConfig, DataChannel};
use webrtc_dtls::config::Config as DtlsConfig;
use webrtc_dtls::conn::DTLSConn;
use webrtc_dtls::crypto::Certificate;
use webrtc_sctp::association::{Association, Config as SctpConfig};
use webrtc_util_011::Conn as Conn011;

/// DataChannel label WA Web uses (pre-negotiated, id=0).
const DATA_CHANNEL_LABEL: &str = "pre-negotiated";
/// SCTP-over-DTLS WebRTC port.
const SCTP_PORT: u16 = 5000;

/// Errors a relay-transport consumer can branch on: a `Connect` failure is fatal (the call can't
/// reach the relay), while `Send`/`RecvClosed` are recoverable conditions on an established channel.
#[derive(Debug, thiserror::Error)]
#[non_exhaustive]
pub enum CallTransportError {
    /// The media stack failed to connect to the relay (UDP/DTLS/SCTP/DataChannel setup). Fatal.
    #[error("relay connect failed: {0}")]
    Connect(#[source] anyhow::Error),
    /// Writing a packet to the open DataChannel failed. Recoverable (retry / drop the frame).
    #[error("relay datachannel write failed")]
    Send(#[source] webrtc_data::Error),
    /// Reading from the DataChannel failed; the peer/relay likely closed it.
    #[error("relay datachannel read failed")]
    RecvClosed(#[source] webrtc_data::Error),
}

/// Classification of a packet seen on the relay channel, by its first byte.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RelayPacketKind {
    Stun,
    Rtcp,
    Rtp,
    Other,
}

/// First-byte demux: top two bits zero means STUN; 0x80/0x81 means RTCP; 0x90 means RTP
/// (WARP); anything else is other.
pub fn classify_relay_packet(data: &[u8]) -> RelayPacketKind {
    if data.len() < 2 {
        return RelayPacketKind::Other;
    }
    let first = data[0];
    if first & 0xc0 != 0 {
        return match first {
            0x80 | 0x81 => RelayPacketKind::Rtcp,
            0x90 => RelayPacketKind::Rtp,
            _ => RelayPacketKind::Other,
        };
    }
    RelayPacketKind::Stun
}

/// Bridges the util-0.11 `Conn` produced by `webrtc-dtls` to the util-0.17 `Conn` consumed
/// by `webrtc-sctp`. The two traits are identical across the version gap, so this is pure
/// delegation with error remapping.
struct DtlsToSctpConn(Arc<DTLSConn>);

fn remap(e: webrtc_util_011::Error) -> webrtc_util::Error {
    webrtc_util::Error::Other(e.to_string())
}

#[async_trait]
impl webrtc_util::Conn for DtlsToSctpConn {
    async fn connect(&self, addr: SocketAddr) -> Result<(), webrtc_util::Error> {
        Conn011::connect(&*self.0, addr).await.map_err(remap)
    }
    async fn recv(&self, buf: &mut [u8]) -> Result<usize, webrtc_util::Error> {
        Conn011::recv(&*self.0, buf).await.map_err(remap)
    }
    async fn recv_from(&self, buf: &mut [u8]) -> Result<(usize, SocketAddr), webrtc_util::Error> {
        Conn011::recv_from(&*self.0, buf).await.map_err(remap)
    }
    async fn send(&self, buf: &[u8]) -> Result<usize, webrtc_util::Error> {
        Conn011::send(&*self.0, buf).await.map_err(remap)
    }
    async fn send_to(&self, buf: &[u8], target: SocketAddr) -> Result<usize, webrtc_util::Error> {
        Conn011::send_to(&*self.0, buf, target).await.map_err(remap)
    }
    fn local_addr(&self) -> Result<SocketAddr, webrtc_util::Error> {
        Conn011::local_addr(&*self.0).map_err(remap)
    }
    fn remote_addr(&self) -> Option<SocketAddr> {
        Conn011::remote_addr(&*self.0)
    }
    async fn close(&self) -> Result<(), webrtc_util::Error> {
        Conn011::close(&*self.0).await.map_err(remap)
    }
    fn as_any(&self) -> &(dyn std::any::Any + Send + Sync) {
        self
    }
}

/// An open relay media channel: STUN/RTP/RTCP travel as binary DataChannel messages.
pub struct RelayMediaChannel {
    dc: Arc<DataChannel>,
}

impl RelayMediaChannel {
    /// Send one media/STUN packet as a binary DataChannel message.
    pub async fn send(&self, data: &[u8]) -> Result<usize, CallTransportError> {
        self.dc
            .write(&Bytes::copy_from_slice(data))
            .await
            .map_err(CallTransportError::Send)
    }

    /// Receive one DataChannel message into `buf`, returning its length.
    pub async fn recv(&self, buf: &mut [u8]) -> Result<usize, CallTransportError> {
        self.dc
            .read(buf)
            .await
            .map_err(CallTransportError::RecvClosed)
    }
}

/// Connect the full media stack to one relay endpoint. Deferred for live validation.
pub async fn connect_relay_media(
    relay_addr: SocketAddr,
) -> Result<RelayMediaChannel, CallTransportError> {
    connect_relay_media_inner(relay_addr)
        .await
        .map_err(CallTransportError::Connect)
}

/// The multi-step setup, kept on `anyhow` for per-step `.context()`; the public boundary wraps the
/// aggregate into [`CallTransportError::Connect`].
async fn connect_relay_media_inner(relay_addr: SocketAddr) -> Result<RelayMediaChannel> {
    // 1. UDP socket connected to the relay.
    let udp = UdpSocket::bind("0.0.0.0:0").await.context("bind udp")?;
    udp.connect(relay_addr)
        .await
        .context("connect udp to relay")?;
    let udp: Arc<dyn Conn011 + Send + Sync> = Arc::new(udp);

    // 2. DTLS client. Self-signed cert (relay does not validate the client cert); skip
    //    server-cert verification (the SDP fingerprint is fixed/cosmetic; media auth is HBH SRTP).
    let cert = Certificate::generate_self_signed(vec!["wa-voip".to_owned()])
        .map_err(|e| anyhow!("dtls self-signed cert: {e}"))?;
    let dtls_config = DtlsConfig {
        certificates: vec![cert],
        insecure_skip_verify: true,
        ..Default::default()
    };
    let dtls = DTLSConn::new(udp, dtls_config, true, None)
        .await
        .map_err(|e| anyhow!("dtls handshake: {e}"))?;
    let net_conn: Arc<dyn webrtc_util::Conn + Send + Sync> =
        Arc::new(DtlsToSctpConn(Arc::new(dtls)));

    // 3. SCTP association (client) over the DTLS connection.
    let assoc = Association::client(SctpConfig {
        net_conn,
        max_receive_buffer_size: 0,
        max_message_size: 0,
        name: "wa-voip".to_owned(),
        remote_port: SCTP_PORT,
        local_port: SCTP_PORT,
    })
    .await
    .map_err(|e| anyhow!("sctp client: {e}"))?;

    // 4. Pre-negotiated DataChannel id=0.
    let dc = DataChannel::dial(
        &Arc::new(assoc),
        0,
        DcConfig {
            negotiated: true,
            label: DATA_CHANNEL_LABEL.to_owned(),
            ..Default::default()
        },
    )
    .await
    .map_err(|e| anyhow!("datachannel dial: {e}"))?;

    Ok(RelayMediaChannel { dc: Arc::new(dc) })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn classify_first_byte() {
        assert_eq!(classify_relay_packet(&[0x00, 0x01]), RelayPacketKind::Stun);
        assert_eq!(classify_relay_packet(&[0x00, 0x03]), RelayPacketKind::Stun);
        assert_eq!(classify_relay_packet(&[0x80, 0xc8]), RelayPacketKind::Rtcp);
        assert_eq!(classify_relay_packet(&[0x81, 0xc8]), RelayPacketKind::Rtcp);
        assert_eq!(classify_relay_packet(&[0x90, 0x78]), RelayPacketKind::Rtp);
        assert_eq!(classify_relay_packet(&[0xff, 0xff]), RelayPacketKind::Other);
        assert_eq!(classify_relay_packet(&[0x00]), RelayPacketKind::Other);
    }
}
```

## Go envelope (signatures only)

```go
package relay

import "net"

type RelayPacketKind int

const (
	RelayPacketStun RelayPacketKind = iota
	RelayPacketRtcp
	RelayPacketRtp
	RelayPacketOther
)

const (
	DataChannelLabel = "pre-negotiated"
	SctpPort         = 5000
)

func ClassifyRelayPacket(data []byte) RelayPacketKind

type RelayMediaChannel struct {
	// unexported handle to the open DataChannel
}

func (c *RelayMediaChannel) Send(data []byte) (int, error)

func (c *RelayMediaChannel) Recv(buf []byte) (int, error)

func ConnectRelayMedia(relayAddr *net.UDPAddr) (*RelayMediaChannel, error)
```

## Implementation suggestions (guidance, not authoritative)

- `classify_relay_packet` is the only pure, fully-specified piece here: the inline
  assertions pin every branch (`<2` bytes → Other, `&0xc0==0` → Stun, `0x80/0x81`
  → Rtcp, `0x90` → Rtp, else Other). Port it first; it is the only part that can be
  unit-tested without a network.
- `u16` port → Go `uint16` (or untyped const). Lengths are `usize` → Go `int`. The
  byte mask `0xc0` and the exact-match arms must stay exact.
- The reference now models transport failures with a typed `CallTransportError`
  (Connect = fatal, Send/RecvClosed = recoverable on an open channel). A Go port can
  mirror that with a small error type or sentinel errors; the public methods return
  `(int, error)` / `(*RelayMediaChannel, error)`.
- The whole connection path is a binding to a specific WebRTC stack
  (UDP→DTLS→SCTP→DataChannel) with hardcoded knobs: `insecure_skip_verify`,
  self-signed cert CN `"wa-voip"`, both SCTP ports `5000`, DataChannel `id=0`,
  `negotiated=true`, label `"pre-negotiated"`. `TODO(human):` pick the Go WebRTC
  stack (e.g. pion/dtls, pion/sctp, pion/datachannel) and decide whether to drive
  the sub-layers directly as here or via a higher-level API — **and** whether the
  project's protobuf-only dependency policy admits these libraries.
- `TODO(human):` the cross-version `Conn` bridge (`DtlsToSctpConn`) exists only
  because two Rust crate versions expose the same trait twice. A Go port likely
  has one `net.Conn`-shaped interface and will not need this adapter — confirm the
  chosen DTLS conn type satisfies the SCTP layer's expected interface directly.
- `Result<usize, E>` → `(int, error)`; `anyhow` context strings → wrapped errors
  (`fmt.Errorf("datachannel write: %w", err)`). Prefer error returns over panics
  throughout, matching the Rust.
- `TODO(human):` `connect_relay_media` is never run in CI and has no vector. Treat
  the byte counts and ordering as a sketch to be confirmed against a live relay,
  not as proven behavior.
- This module is the most human-decision-heavy of the set: the network/library
  glue is not pinned by any test vector and must be validated against a real
  endpoint before it can be trusted.
