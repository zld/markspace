//! The normalized event model shared by parsers, store, correlator and
//! detector.
//!
//! All timestamps are UTC microseconds since the Unix epoch. Optional fields
//! use [`Option`] and are serialized as explicit `null` rather than being
//! faked as zero values.

use serde::{Deserialize, Serialize};

/// Input format a line was decoded from.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum InputFormat {
    JsonLine,
    Syslog,
}

impl InputFormat {
    pub fn as_str(self) -> &'static str {
        match self {
            InputFormat::JsonLine => "json_line",
            InputFormat::Syslog => "syslog",
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub enum Protocol {
    Tcp,
    Udp,
    Icmp,
}

impl Protocol {
    pub fn parse(s: &str) -> Option<Protocol> {
        match s.trim().to_ascii_lowercase().as_str() {
            "tcp" => Some(Protocol::Tcp),
            "udp" => Some(Protocol::Udp),
            "icmp" => Some(Protocol::Icmp),
            _ => None,
        }
    }

    pub fn as_str(self) -> &'static str {
        match self {
            Protocol::Tcp => "tcp",
            Protocol::Udp => "udp",
            Protocol::Icmp => "icmp",
        }
    }
}

/// Policy/gateway decision recorded on the event.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub enum Action {
    Allow,
    Deny,
    AuthOk,
    AuthFail,
    Connect,
    Disconnect,
    Data,
}

impl Action {
    pub fn parse(s: &str) -> Option<Action> {
        match s.trim().to_ascii_lowercase().as_str() {
            "allow" => Some(Action::Allow),
            "deny" => Some(Action::Deny),
            "auth_ok" | "authok" | "success" => Some(Action::AuthOk),
            "auth_fail" | "authfail" | "failure" | "fail" => Some(Action::AuthFail),
            "connect" | "syn" => Some(Action::Connect),
            "disconnect" | "fin" | "rst" => Some(Action::Disconnect),
            "data" => Some(Action::Data),
            _ => None,
        }
    }

    pub fn as_str(self) -> &'static str {
        match self {
            Action::Allow => "allow",
            Action::Deny => "deny",
            Action::AuthOk => "auth_ok",
            Action::AuthFail => "auth_fail",
            Action::Connect => "connect",
            Action::Disconnect => "disconnect",
            Action::Data => "data",
        }
    }
}

/// A fully normalized gateway event.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Event {
    /// Originating device/gateway identifier.
    pub device: String,
    /// UTC microseconds since Unix epoch.
    pub ts_us: i64,
    pub src_addr: String,
    /// `None` when the source record genuinely has no port (e.g. ICMP).
    pub src_port: Option<u16>,
    pub dst_addr: String,
    pub dst_port: Option<u16>,
    pub protocol: Protocol,
    pub action: Action,
    /// Policy/rule label emitted by the gateway, if any.
    pub rule_label: Option<String>,
    /// Account name on authentication events, otherwise absent.
    pub account: Option<String>,
    /// Bytes observed in the event (data events only).
    pub bytes: Option<u64>,
    /// Identifier supplied by the source device, if present.
    pub event_id: Option<String>,
    /// FNV-1a 64-bit hex digest of the raw input line.
    pub raw_digest: String,
    /// FNV-1a 64-bit hex digest over the normalized field set — stable under
    /// batch replay and independent of formatting/whitespace.
    pub content_hash: String,
    pub source_format: InputFormat,
    /// Ground-truth anomaly label embedded by the fixture generator. Never
    /// read by the detector; only the terminal evaluation joins on it.
    pub truth: Option<String>,
}

impl Event {
    /// Canonical 5-tuple key used for session correlation. The initiator side
    /// is derived from the first connect/data event, not assumed from address
    /// ordering, so bidirectional traffic merges correctly.
    pub fn five_tuple(&self) -> FiveTuple {
        FiveTuple {
            device: self.device.clone(),
            proto: self.protocol,
            a_addr: self.src_addr.clone(),
            a_port: self.src_port,
            b_addr: self.dst_addr.clone(),
            b_port: self.dst_port,
        }
    }
}

/// Connection 5-tuple. Endpoints are stored in the order first observed; the
/// same reversed pair maps to the same session via [`FiveTuple::key`].
#[derive(Debug, Clone, PartialEq, Eq, Hash)]
pub struct FiveTuple {
    pub device: String,
    pub proto: Protocol,
    pub a_addr: String,
    pub a_port: Option<u16>,
    pub b_addr: String,
    pub b_port: Option<u16>,
}

impl FiveTuple {
    /// Direction-independent canonical key: sort the two endpoint strings so
    /// forward and reverse events collide.
    pub fn key(&self) -> String {
        let e1 = format!("{}:{}", self.a_addr, port_str(self.a_port));
        let e2 = format!("{}:{}", self.b_addr, port_str(self.b_port));
        let (lo, hi) = if e1 <= e2 { (e1, e2) } else { (e2, e1) };
        format!("{}|{}|{}|{}", self.device, self.proto.as_str(), lo, hi)
    }
}

fn port_str(p: Option<u16>) -> String {
    match p {
        Some(v) => v.to_string(),
        None => "-".to_string(),
    }
}

/// FNV-1a 64-bit hash, returned as 16-char lowercase hex. This is a content
/// digest for dedup/evidence only — not a cryptographic control.
pub fn fnv1a_64_hex(bytes: &[u8]) -> String {
    const OFFSET: u64 = 0xcbf29ce484222325;
    const PRIME: u64 = 0x100000001b3;
    let mut h = OFFSET;
    for &b in bytes {
        h ^= b as u64;
        h = h.wrapping_mul(PRIME);
    }
    format!("{:016x}", h)
}

/// Compute the normalized content hash for an event. The digest excludes the
/// raw digest, source formatting and ground-truth label, so the same logical
/// event arriving twice (replayed batch) hashes identically.
#[allow(clippy::too_many_arguments)]
pub fn content_hash_of(
    device: &str,
    ts_us: i64,
    src_addr: &str,
    src_port: Option<u16>,
    dst_addr: &str,
    dst_port: Option<u16>,
    protocol: Protocol,
    action: Action,
    rule_label: &Option<String>,
    account: &Option<String>,
    bytes: Option<u64>,
    event_id: &Option<String>,
) -> String {
    let mut s = String::with_capacity(160);
    s.push_str(device);
    s.push('|');
    s.push_str(&ts_us.to_string());
    s.push('|');
    s.push_str(src_addr);
    s.push('|');
    s.push_str(&port_str(src_port));
    s.push('|');
    s.push_str(dst_addr);
    s.push('|');
    s.push_str(&port_str(dst_port));
    s.push('|');
    s.push_str(protocol.as_str());
    s.push('|');
    s.push_str(action.as_str());
    s.push('|');
    s.push_str(rule_label.as_deref().unwrap_or("-"));
    s.push('|');
    s.push_str(account.as_deref().unwrap_or("-"));
    s.push('|');
    match bytes {
        Some(b) => s.push_str(&b.to_string()),
        None => s.push('-'),
    }
    s.push('|');
    s.push_str(event_id.as_deref().unwrap_or("-"));
    fnv1a_64_hex(s.as_bytes())
}
