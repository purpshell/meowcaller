# Datasheet: `signaling/stanza`

Outbound call-signaling builders (offer / accept / preaccept / transport /
relaylatency / heartbeat / terminate / mute / reject) that assemble the call
control stanzas. Signaling layer.

**Validation vector:** (integration — no single vector). Pinned by the inline
`#[test]` cases in the reference source below (child-order, multi-device
destination, accept/preaccept shape, transport net/protocol rule, relaylatency
latency encoding, heartbeat addressing, terminate targets).

**Reference pinned at:** `41095d4e6ba4610e054e9ede3af1d5e88a83faee` (whatsapp-rust `wacore/src/voip/`).

## Reference source (verbatim — authoritative)

```rust
//! Outbound call signaling builders (offer/accept/preaccept/transport/relaylatency/
//! heartbeat/terminate/mute/reject) as free `Node` builders. The `<offer>` child order
//! is load-bearing (server returns 439 if wrong).
//!
//! Stanza ids generated from random bytes (heartbeat, preaccept) are passed in
//! so the builders stay pure; the I/O layer supplies them.

use wacore_binary::builder::NodeBuilder;
use wacore_binary::{Jid, Node};

/// Capability blob for `<offer>`/`<accept>` (ver=1).
pub const CAPABILITY_OFFER: [u8; 7] = [0x01, 0x05, 0xf7, 0x09, 0xe4, 0xbb, 0x13];
/// Capability blob for `<preaccept>` (ver=1).
pub const CAPABILITY_PREACCEPT: [u8; 7] = [0x01, 0x05, 0xf7, 0x09, 0xe4, 0xbb, 0x07];

/// Relay latency wire encoding: `0x2000000 + rtt_ms`.
pub fn encode_latency(rtt_ms: u32) -> String {
    (0x0200_0000u32.wrapping_add(rtt_ms)).to_string()
}

/// One per-device encrypted callKey entry inside `<offer>`.
pub struct OfferDeviceKey {
    pub device_jid: Jid,
    pub ciphertext: Vec<u8>,
    /// Signal message type: `pkmsg` or `msg`.
    pub enc_type: String,
}

pub struct OfferParams<'a> {
    pub call_id: &'a str,
    pub to: &'a Jid,
    pub call_creator: &'a Jid,
    pub device_keys: &'a [OfferDeviceKey],
    pub privacy_token: Option<&'a [u8]>,
    pub capability: Option<&'a [u8]>,
    pub device_identity: Option<&'a [u8]>,
}

/// `<call to=peer><offer call-id call-creator>…</offer></call>` with the mandatory
/// child order: privacy → audio(8k) → audio(16k) → net → capability → destination|enc →
/// encopt → device-identity.
pub fn build_offer(p: &OfferParams<'_>) -> Node {
    let mut children: Vec<Node> = Vec::new();
    if let Some(privacy) = p.privacy_token {
        children.push(NodeBuilder::new("privacy").bytes(privacy.to_vec()).build());
    }
    children.push(
        NodeBuilder::new("audio")
            .attr("enc", "opus")
            .attr("rate", "8000")
            .build(),
    );
    children.push(
        NodeBuilder::new("audio")
            .attr("enc", "opus")
            .attr("rate", "16000")
            .build(),
    );
    children.push(NodeBuilder::new("net").attr("medium", "3").build());
    if let Some(cap) = p.capability {
        children.push(
            NodeBuilder::new("capability")
                .attr("ver", "1")
                .bytes(cap.to_vec())
                .build(),
        );
    }

    if p.device_keys.len() > 1 {
        let to_nodes: Vec<Node> = p
            .device_keys
            .iter()
            .map(|dk| {
                NodeBuilder::new("to")
                    .attr("jid", &dk.device_jid)
                    .children([enc_node(dk)])
                    .build()
            })
            .collect();
        children.push(NodeBuilder::new("destination").children(to_nodes).build());
    } else if let Some(dk) = p.device_keys.first() {
        children.push(enc_node(dk));
    }

    children.push(NodeBuilder::new("encopt").attr("keygen", "2").build());
    if let Some(di) = p.device_identity {
        children.push(
            NodeBuilder::new("device-identity")
                .bytes(di.to_vec())
                .build(),
        );
    }

    call_wrap(
        p.to,
        None,
        offer_action("offer", p.call_id, p.call_creator, children),
    )
}

fn enc_node(dk: &OfferDeviceKey) -> Node {
    NodeBuilder::new("enc")
        .attr("v", "2")
        .attr("type", dk.enc_type.clone())
        .attr("count", "0")
        .bytes(dk.ciphertext.clone())
        .build()
}

pub struct AcceptParams<'a> {
    pub call_id: &'a str,
    pub to: &'a Jid,
    pub call_creator: &'a Jid,
    /// Advertised `<audio enc=opus rate=…>` formats, in preference order. Selecting only `8000`
    /// is the lever to steer the caller off Meta's 16 kHz mlow codec onto RFC Opus NB.
    pub audio_rates: &'a [&'a str],
    pub relay_te: Option<&'a [u8]>,
    pub rte: Option<&'a [u8]>,
    pub voip_settings: Option<&'a [u8]>,
    pub capability: Option<&'a [u8]>,
}

/// `<accept>`: audio → [te priority=2] → net medium=2 → encopt → [capability] → [rte] → [voip_settings].
pub fn build_accept(p: &AcceptParams<'_>) -> Node {
    let mut children: Vec<Node> = p.audio_rates.iter().map(|rate| audio_opus(rate)).collect();
    if let Some(te) = p.relay_te {
        children.push(
            NodeBuilder::new("te")
                .attr("priority", "2")
                .bytes(te.to_vec())
                .build(),
        );
    }
    children.push(NodeBuilder::new("net").attr("medium", "2").build());
    children.push(NodeBuilder::new("encopt").attr("keygen", "2").build());
    if let Some(cap) = p.capability {
        children.push(
            NodeBuilder::new("capability")
                .attr("ver", "1")
                .bytes(cap.to_vec())
                .build(),
        );
    }
    if let Some(rte) = p.rte {
        children.push(NodeBuilder::new("rte").bytes(rte.to_vec()).build());
    }
    if let Some(vs) = p.voip_settings {
        children.push(
            NodeBuilder::new("voip_settings")
                .attr("uncompressed", "1")
                .bytes(vs.to_vec())
                .build(),
        );
    }
    call_wrap(
        p.to,
        None,
        offer_action("accept", p.call_id, p.call_creator, children),
    )
}

/// One `<audio enc=opus rate=…>` advertisement child.
fn audio_opus(rate: &str) -> Node {
    NodeBuilder::new("audio")
        .attr("enc", "opus")
        .attr("rate", rate)
        .build()
}

/// `<preaccept>`: audio → encopt → capability(preaccept blob). `id` is the random call-wrapper id.
pub fn build_preaccept(
    call_id: &str,
    to: &Jid,
    call_creator: &Jid,
    wrapper_id: &str,
    audio_rates: &[&str],
) -> Node {
    let mut children: Vec<Node> = audio_rates.iter().map(|rate| audio_opus(rate)).collect();
    children.push(NodeBuilder::new("encopt").attr("keygen", "2").build());
    children.push(
        NodeBuilder::new("capability")
            .attr("ver", "1")
            .bytes(CAPABILITY_PREACCEPT.to_vec())
            .build(),
    );
    call_wrap(
        to,
        Some(wrapper_id),
        offer_action("preaccept", call_id, call_creator, children),
    )
}

pub struct TransportParams<'a> {
    pub call_id: &'a str,
    pub to: &'a Jid,
    pub call_creator: &'a Jid,
    pub p2p_cand_round: Option<&'a str>,
    pub transport_message_type: Option<&'a str>,
    pub relay_te: Option<&'a [u8]>,
}

/// `<transport>`: optional `<te priority=1>` then `<net medium=2 [protocol=0]>`.
pub fn build_transport(p: &TransportParams<'_>) -> Node {
    let mut action = NodeBuilder::new("transport")
        .attr("call-id", p.call_id)
        .attr("call-creator", p.call_creator);
    if let Some(round) = p.p2p_cand_round {
        action = action.attr("p2p-cand-round", round.to_string());
    }
    if let Some(mt) = p.transport_message_type {
        action = action.attr("transport-message-type", mt.to_string());
    }

    let mut children: Vec<Node> = Vec::new();
    if let Some(te) = p.relay_te {
        children.push(
            NodeBuilder::new("te")
                .attr("priority", "1")
                .bytes(te.to_vec())
                .build(),
        );
    }
    let mut net = NodeBuilder::new("net").attr("medium", "2");
    if p.transport_message_type != Some("9") {
        net = net.attr("protocol", "0");
    }
    children.push(net.build());

    call_wrap(p.to, None, action.children(children).build())
}

pub struct RelayLatencyParams<'a> {
    pub call_id: &'a str,
    pub to: &'a Jid,
    pub call_creator: &'a Jid,
    pub latency_ms: u32,
    pub relay_name: &'a str,
    pub address_bytes: &'a [u8],
    /// Peer devices; omit for inbound callee.
    pub devices: &'a [Jid],
}

/// `<relaylatency>` with a `<te latency relay_name>` and optional `<destination>`.
pub fn build_relay_latency(p: &RelayLatencyParams<'_>) -> Node {
    let mut children: Vec<Node> = vec![
        NodeBuilder::new("te")
            .attr("latency", encode_latency(p.latency_ms))
            .attr("relay_name", p.relay_name.to_string())
            .bytes(p.address_bytes.to_vec())
            .build(),
    ];
    if !p.devices.is_empty() {
        children.push(destination_to(p.devices));
    }
    call_wrap(
        p.to,
        None,
        offer_action("relaylatency", p.call_id, p.call_creator, children),
    )
}

/// `<call to={call_id}@call id=…><heartbeat call-id call-creator/></call>`.
pub fn build_heartbeat(call_id: &str, call_creator: &Jid, wrapper_id: &str) -> Node {
    let action = NodeBuilder::new("heartbeat")
        .attr("call-id", call_id)
        .attr("call-creator", call_creator)
        .build();
    NodeBuilder::new("call")
        .attr("to", format!("{call_id}@call"))
        .attr("id", wrapper_id.to_string())
        .children([action])
        .build()
}

pub struct TerminateParams<'a> {
    pub call_id: &'a str,
    pub to: &'a Jid,
    pub call_creator: &'a Jid,
    pub reason: Option<&'a str>,
    /// Other peer devices to hang up (accepted_elsewhere).
    pub target_devices: &'a [Jid],
}

pub fn build_terminate(p: &TerminateParams<'_>) -> Node {
    let mut action = NodeBuilder::new("terminate")
        .attr("call-id", p.call_id)
        .attr("call-creator", p.call_creator);
    if let Some(reason) = p.reason {
        action = action.attr("reason", reason.to_string());
    }
    if !p.target_devices.is_empty() {
        action = action.children([destination_to(p.target_devices)]);
    }
    call_wrap(p.to, None, action.build())
}

pub fn build_mute_v2(call_id: &str, to: &Jid, call_creator: &Jid, mute_state: &str) -> Node {
    let action = NodeBuilder::new("mute_v2")
        .attr("call-id", call_id)
        .attr("call-creator", call_creator)
        .attr("mute-state", mute_state.to_string())
        .build();
    call_wrap(to, None, action)
}

pub fn build_reject(call_id: &str, to: &Jid, call_creator: &Jid) -> Node {
    call_wrap(
        to,
        None,
        NodeBuilder::new("reject")
            .attr("call-id", call_id)
            .attr("call-creator", call_creator)
            .build(),
    )
}

fn offer_action(tag: &'static str, call_id: &str, call_creator: &Jid, children: Vec<Node>) -> Node {
    NodeBuilder::new(tag)
        .attr("call-id", call_id)
        .attr("call-creator", call_creator)
        .children(children)
        .build()
}

fn destination_to(devices: &[Jid]) -> Node {
    let tos: Vec<Node> = devices
        .iter()
        .map(|jid| NodeBuilder::new("to").attr("jid", jid).build())
        .collect();
    NodeBuilder::new("destination").children(tos).build()
}

fn call_wrap(to: &Jid, id: Option<&str>, action: Node) -> Node {
    let mut call = NodeBuilder::new("call").attr("to", to);
    if let Some(id) = id {
        call = call.attr("id", id.to_string());
    }
    call.children([action]).build()
}

#[cfg(test)]
mod tests {
    use super::*;
    use wacore_binary::{NodeRef, Server};

    fn peer() -> Jid {
        Jid::new("214482127208608", Server::Lid)
    }
    fn creator() -> Jid {
        Jid::new("243426515787784", Server::Lid).with_device(19)
    }

    fn child_tags(call: &Node) -> Vec<String> {
        let r: NodeRef<'_> = call.as_node_ref();
        let action = &r.children().unwrap()[0];
        action
            .children()
            .unwrap()
            .iter()
            .map(|c| c.tag.as_ref().to_string())
            .collect()
    }

    #[test]
    fn offer_child_order_is_load_bearing() {
        let peer = peer();
        let creator = creator();
        let dk = OfferDeviceKey {
            device_jid: peer.clone(),
            ciphertext: vec![1, 2, 3],
            enc_type: "pkmsg".into(),
        };
        let call = build_offer(&OfferParams {
            call_id: "CID",
            to: &peer,
            call_creator: &creator,
            device_keys: std::slice::from_ref(&dk),
            privacy_token: Some(&[0xaa, 0xbb]),
            capability: Some(&CAPABILITY_OFFER),
            device_identity: Some(&[0xcc]),
        });
        // Single device key → bare <enc> (not <destination>).
        assert_eq!(
            child_tags(&call),
            [
                "privacy",
                "audio",
                "audio",
                "net",
                "capability",
                "enc",
                "encopt",
                "device-identity"
            ]
        );
        let r = call.as_node_ref();
        assert_eq!(r.tag.as_ref(), "call");
        let offer = &r.children().unwrap()[0];
        assert_eq!(offer.tag.as_ref(), "offer");
        assert_eq!(
            offer.attrs().optional_string("call-id").as_deref(),
            Some("CID")
        );
    }

    #[test]
    fn offer_multi_device_uses_destination() {
        let peer = peer();
        let creator = creator();
        let keys = vec![
            OfferDeviceKey {
                device_jid: peer.clone(),
                ciphertext: vec![1],
                enc_type: "pkmsg".into(),
            },
            OfferDeviceKey {
                device_jid: creator.clone(),
                ciphertext: vec![2],
                enc_type: "msg".into(),
            },
        ];
        let call = build_offer(&OfferParams {
            call_id: "CID",
            to: &peer,
            call_creator: &creator,
            device_keys: &keys,
            privacy_token: None,
            capability: None,
            device_identity: None,
        });
        let tags = child_tags(&call);
        assert!(tags.contains(&"destination".to_string()));
        assert!(!tags.contains(&"enc".to_string()));
    }

    #[test]
    fn accept_and_preaccept_shape() {
        let peer = peer();
        let creator = creator();
        let accept = build_accept(&AcceptParams {
            call_id: "CID",
            to: &peer,
            call_creator: &creator,
            audio_rates: &["16000"],
            relay_te: Some(&[0u8; 6]),
            rte: None,
            voip_settings: None,
            capability: Some(&CAPABILITY_OFFER),
        });
        assert_eq!(
            child_tags(&accept),
            ["audio", "te", "net", "encopt", "capability"]
        );

        let pre = build_preaccept("CID", &peer, &creator, "abcd1234", &["8000", "16000"]);
        assert_eq!(child_tags(&pre), ["audio", "audio", "encopt", "capability"]);
        assert_eq!(
            pre.as_node_ref().attrs().optional_string("id").as_deref(),
            Some("abcd1234")
        );
    }

    #[test]
    fn transport_net_protocol_rule() {
        let peer = peer();
        let creator = creator();
        // type != 9 → net has protocol=0
        let t1 = build_transport(&TransportParams {
            call_id: "CID",
            to: &peer,
            call_creator: &creator,
            p2p_cand_round: Some("1"),
            transport_message_type: Some("1"),
            relay_te: Some(&[9u8; 6]),
        });
        let r = t1.as_node_ref();
        let action = &r.children().unwrap()[0];
        assert_eq!(
            action
                .attrs()
                .optional_string("transport-message-type")
                .as_deref(),
            Some("1")
        );
        let net = action.get_optional_child("net").unwrap();
        assert_eq!(
            net.attrs().optional_string("protocol").as_deref(),
            Some("0")
        );

        // type == 9 → no protocol attr
        let t9 = build_transport(&TransportParams {
            call_id: "CID",
            to: &peer,
            call_creator: &creator,
            p2p_cand_round: None,
            transport_message_type: Some("9"),
            relay_te: None,
        });
        let r9 = t9.as_node_ref();
        let net9 = r9.children().unwrap()[0].get_optional_child("net").unwrap();
        assert!(net9.attrs().optional_string("protocol").is_none());
    }

    #[test]
    fn relaylatency_encoding_and_heartbeat() {
        let peer = peer();
        let creator = creator();
        assert_eq!(encode_latency(45), "33554477");
        let rl = build_relay_latency(&RelayLatencyParams {
            call_id: "CID",
            to: &peer,
            call_creator: &creator,
            latency_ms: 45,
            relay_name: "gru1c02",
            address_bytes: &[1, 2, 3, 4, 5, 6],
            devices: std::slice::from_ref(&peer),
        });
        let r = rl.as_node_ref();
        let action = &r.children().unwrap()[0];
        let te = action.get_optional_child("te").unwrap();
        assert_eq!(
            te.attrs().optional_string("latency").as_deref(),
            Some("33554477")
        );
        assert_eq!(
            te.attrs().optional_string("relay_name").as_deref(),
            Some("gru1c02")
        );
        assert!(action.get_optional_child("destination").is_some());

        let hb = build_heartbeat("CALLID", &creator, "DEADBEEF");
        assert_eq!(
            hb.as_node_ref().attrs().optional_string("to").as_deref(),
            Some("CALLID@call")
        );
        assert_eq!(
            hb.as_node_ref().attrs().optional_string("id").as_deref(),
            Some("DEADBEEF")
        );
    }

    #[test]
    fn terminate_with_targets() {
        let peer = peer();
        let creator = creator();
        let term = build_terminate(&TerminateParams {
            call_id: "CID",
            to: &peer,
            call_creator: &creator,
            reason: Some("accepted_elsewhere"),
            target_devices: std::slice::from_ref(&peer),
        });
        let r = term.as_node_ref();
        let action = &r.children().unwrap()[0];
        assert_eq!(
            action.attrs().optional_string("reason").as_deref(),
            Some("accepted_elsewhere")
        );
        assert!(action.get_optional_child("destination").is_some());
    }
}
```

## Go envelope (signatures only)

```go
package signaling

import "meowcaller/binary" // Jid, Node, NodeBuilder

// CapabilityOffer is the capability blob for <offer>/<accept> (ver=1).
var CapabilityOffer = [7]byte{0x01, 0x05, 0xf7, 0x09, 0xe4, 0xbb, 0x13}

// CapabilityPreaccept is the capability blob for <preaccept> (ver=1).
var CapabilityPreaccept = [7]byte{0x01, 0x05, 0xf7, 0x09, 0xe4, 0xbb, 0x07}

func EncodeLatency(rttMs uint32) string

type OfferDeviceKey struct {
	DeviceJid  binary.Jid
	Ciphertext []byte
	EncType    string // "pkmsg" or "msg"
}

type OfferParams struct {
	CallID         string
	To             *binary.Jid
	CallCreator    *binary.Jid
	DeviceKeys     []OfferDeviceKey
	PrivacyToken   []byte // nil = absent
	Capability     []byte // nil = absent
	DeviceIdentity []byte // nil = absent
}

func BuildOffer(p *OfferParams) binary.Node

type AcceptParams struct {
	CallID       string
	To           *binary.Jid
	CallCreator  *binary.Jid
	AudioRates   []string
	RelayTe      []byte // nil = absent
	Rte          []byte // nil = absent
	VoipSettings []byte // nil = absent
	Capability   []byte // nil = absent
}

func BuildAccept(p *AcceptParams) binary.Node

func BuildPreaccept(callID string, to, callCreator *binary.Jid, wrapperID string, audioRates []string) binary.Node

type TransportParams struct {
	CallID               string
	To                   *binary.Jid
	CallCreator          *binary.Jid
	P2PCandRound         *string // nil = absent
	TransportMessageType *string // nil = absent
	RelayTe              []byte  // nil = absent
}

func BuildTransport(p *TransportParams) binary.Node

type RelayLatencyParams struct {
	CallID       string
	To           *binary.Jid
	CallCreator  *binary.Jid
	LatencyMs    uint32
	RelayName    string
	AddressBytes []byte
	Devices      []binary.Jid
}

func BuildRelayLatency(p *RelayLatencyParams) binary.Node

func BuildHeartbeat(callID string, callCreator *binary.Jid, wrapperID string) binary.Node

type TerminateParams struct {
	CallID        string
	To            *binary.Jid
	CallCreator   *binary.Jid
	Reason        *string // nil = absent
	TargetDevices []binary.Jid
}

func BuildTerminate(p *TerminateParams) binary.Node

func BuildMuteV2(callID string, to, callCreator *binary.Jid, muteState string) binary.Node

func BuildReject(callID string, to, callCreator *binary.Jid) binary.Node
```

## Implementation suggestions (guidance, not authoritative)

- The Rust `Option<&[u8]>` / `Option<&str>` params map to Go nil-able slices and
  `*string` respectively — nil means "omit the child/attr", which is load-bearing
  for the offer child order.
- `u32` → `uint32`; `Vec<u8>`/`&[u8]` → `[]byte`; `&[&str]` → `[]string`. The
  capability constants are fixed-length arrays in Rust; a Go `[7]byte` or a
  package-level `[]byte` both work — `TODO(human)`: pick one and keep it immutable.
- Child ordering is positional: append children to a slice in exactly the source
  order. The single-device-key branch emits a bare `enc` child; more than one key
  emits a `destination` wrapper instead.
- `encode_latency` uses `wrapping_add` on `u32`; Go `uint32` addition already wraps,
  so a plain `0x02000000 + rttMs` then `strconv` is equivalent.
- These are pure builders with no error return; mirror that (no `error` in
  signatures). `TODO(human)`: confirm your `Node`/`NodeBuilder` attr setters accept
  a `*Jid` the way the Rust `.attr("jid", jid)` does.
- The `transport` net child gets `protocol=0` for every `transport-message-type`
  except the literal string `"9"`.
```
