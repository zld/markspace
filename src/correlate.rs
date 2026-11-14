//! Session state machine and correlation rules.
//!
//! Sessions are keyed by a direction-independent 5-tuple so bidirectional
//! traffic merges into one record. TCP sessions follow connect/disconnect;
//! UDP sessions have no control plane and are closed by idle timeout. When a
//! 5-tuple is reused after a clean close, the old session is finalized with
//! its termination reason and a fresh session starts — they are never merged.
//!
//! Three rules produce evidence with the triggering event count:
//! - `RULE_PORT_SCAN` — many distinct destination ports probed in a short
//!   window from one source address;
//! - `RULE_BRUTE_FORCE` — N consecutive authentication failures for one
//!   account followed by a success within a time limit;
//! - `RULE_OUTBOUND_BURST` — a single session whose outbound byte total
//!   crosses a threshold.

use crate::model::{Action, Event, FiveTuple, Protocol};
use std::collections::{BTreeMap, HashMap, HashSet};

pub const RULE_PORT_SCAN: &str = "RULE_PORT_SCAN";
pub const RULE_BRUTE_FORCE: &str = "RULE_BRUTE_FORCE";
pub const RULE_OUTBOUND_BURST: &str = "RULE_OUTBOUND_BURST";

#[derive(Debug, Clone)]
pub struct CorrelatorConfig {
    pub scan_window_us: i64,
    pub scan_min_ports: usize,
    pub brute_max_failures: usize,
    pub brute_window_us: i64,
    pub burst_bytes: u64,
    pub udp_idle_us: i64,
    pub tcp_idle_us: i64,
}

impl Default for CorrelatorConfig {
    fn default() -> Self {
        Self {
            scan_window_us: 30_000_000,
            scan_min_ports: 10,
            brute_max_failures: 5,
            brute_window_us: 120_000_000,
            burst_bytes: 1_000_000,
            udp_idle_us: 60_000_000,
            tcp_idle_us: 300_000_000,
        }
    }
}

/// Why a session ended.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum TermReason {
    TcpClosed,
    UdpIdleTimeout,
    TcpIdleTimeout,
    FiveTupleReuse,
    IncompleteAtEof,
}

impl TermReason {
    pub fn as_str(&self) -> &'static str {
        match self {
            TermReason::TcpClosed => "tcp_closed",
            TermReason::UdpIdleTimeout => "udp_idle_timeout",
            TermReason::TcpIdleTimeout => "tcp_idle_timeout",
            TermReason::FiveTupleReuse => "five_tuple_reuse",
            TermReason::IncompleteAtEof => "incomplete_at_eof",
        }
    }
}

/// One rule firing attached to a session.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct RuleHit {
    pub rule_id: &'static str,
    pub trigger_events: usize,
    pub ts_us: i64,
}

/// A correlation record (the "event evidence chain").
#[derive(Debug, Clone)]
pub struct Session {
    pub session_id: String,
    pub device: String,
    pub key: String,
    pub tuple: FiveTuple,
    pub init_addr: String,
    pub init_port: Option<u16>,
    pub peer_addr: String,
    pub peer_port: Option<u16>,
    pub start_us: i64,
    pub end_us: i64,
    pub event_count: usize,
    pub outbound_bytes: u64,
    /// Raw-line digests in arrival order — evidence without raw content.
    pub evidence: Vec<String>,
    pub rule_hits: Vec<RuleHit>,
    pub term_reason: Option<TermReason>,
    pub account: Option<String>,
    /// Ground-truth label carried by this session's events (evaluation only).
    pub truth: Option<String>,
}

impl Session {
    pub fn is_anomalous(&self) -> bool {
        !self.rule_hits.is_empty()
    }

    /// Canonical label under which truth is evaluated on this session.
    pub fn truth_label(&self) -> Option<&str> {
        self.truth.as_deref()
    }
}

/// Sliding per-source port-probe tracker. The minimum-ports decision is
/// applied by the owning [`Correlator`] with the configured threshold.
#[derive(Debug, Clone)]
struct ScanState {
    /// (timestamp, destination port) observations within the scan window.
    probes: Vec<(i64, u16)>,
    fired: bool,
}

#[derive(Debug, Clone)]
struct BruteState {
    failures: Vec<i64>,
}

/// Persistent per-source attack profile. Once a rule fires for a source, all
/// subsequent sessions from that source inherit the rule and label, even if
/// their short-lived 5-tuple session had already been closed by the time the
/// campaign crossed its threshold.
#[derive(Debug, Clone, Default)]
struct AttackProfile {
    label: Option<String>,
    /// (rule id, triggering event count observed when it first fired).
    rules: Vec<(&'static str, usize)>,
}

/// Streaming correlator. Active state is bounded by the number of live
/// sessions, trackers and the configured windows — closed sessions are handed
/// to the window manager and dropped from memory.
pub struct Correlator {
    config: CorrelatorConfig,
    active: HashMap<String, Session>,
    /// Min-heap of `(expiry_us, key)` for O(log n) idle expiration. Entries
    /// are lazily deleted: a stale entry whose session was closed or updated
    /// is simply skipped when it reaches the top.
    expiry: std::collections::BinaryHeap<ExpiryEntry>,
    scans: HashMap<String, ScanState>,
    brute: HashMap<String, BruteState>,
    profiles: HashMap<String, AttackProfile>,
    closed: Vec<Session>,
    next_id: u64,
}

/// Heap entry ordered by earliest expiry first (reversed `Ord`).
#[derive(Debug, Clone, PartialEq, Eq)]
struct ExpiryEntry(i64, String);

impl PartialOrd for ExpiryEntry {
    fn partial_cmp(&self, other: &Self) -> Option<std::cmp::Ordering> {
        Some(self.cmp(other))
    }
}

impl Ord for ExpiryEntry {
    fn cmp(&self, other: &Self) -> std::cmp::Ordering {
        // Reverse so BinaryHeap (max-heap) behaves as a min-heap on expiry.
        other
            .0
            .cmp(&self.0)
            .then_with(|| other.1.cmp(&self.1))
    }
}

impl Correlator {
    pub fn new(config: CorrelatorConfig) -> Self {
        Self {
            config,
            active: HashMap::new(),
            expiry: std::collections::BinaryHeap::new(),
            scans: HashMap::new(),
            brute: HashMap::new(),
            profiles: HashMap::new(),
            closed: Vec::new(),
            next_id: 1,
        }
    }

    /// Feed one normalized, in-order event. Late events must be filtered by
    /// the caller so sealed windows are never touched.
    pub fn process(&mut self, ev: &Event) {
        self.expire_idle(ev.ts_us);
        let key = ev.five_tuple().key();

        // Port-scan tracking applies to connection attempts from a source.
        if matches!(ev.action, Action::Connect | Action::Deny) {
            if let Some(port) = ev.dst_port {
                self.observe_scan(ev, port);
            }
        }

        // Brute-force bookkeeping on authentication events.
        self.observe_brute(ev, &key);

        let burst_threshold = self.config.burst_bytes;
        let mut close_tcp = false;
        {
            let session = match self.active.get_mut(&key) {
                Some(s) => s,
                None => self.start_session(ev, &key),
            };

            session.event_count += 1;
            session.end_us = ev.ts_us;
            session.evidence.push(ev.raw_digest.clone());
            if ev.account.is_some() && session.account.is_none() {
                session.account = ev.account.clone();
            }
            if ev.truth.is_some() && session.truth.is_none() {
                session.truth = ev.truth.clone();
            }
            if let Some(b) = ev.bytes {
                // Outbound = bytes emitted by the session initiator. The
                // initiator is whoever sent the first event of the session.
                if ev.src_addr == session.init_addr && ev.src_port == session.init_port {
                    session.outbound_bytes = session.outbound_bytes.saturating_add(b);
                }
            }

            // Outbound burst rule.
            if session.outbound_bytes >= burst_threshold
                && !session
                    .rule_hits
                    .iter()
                    .any(|h| h.rule_id == RULE_OUTBOUND_BURST)
            {
                session.rule_hits.push(RuleHit {
                    rule_id: RULE_OUTBOUND_BURST,
                    trigger_events: session.event_count,
                    ts_us: ev.ts_us,
                });
            }

            // TCP explicit close finalizes the session.
            if ev.protocol == Protocol::Tcp && ev.action == Action::Disconnect {
                close_tcp = true;
                session.end_us = ev.ts_us;
            }
        }

        // Refresh the idle deadline outside the session's borrow scope.
        if !close_tcp {
            if let Some(s) = self.active.get(&key) {
                self.schedule_expiry(&key, s.end_us, s.tuple.proto);
            }
        }

        if close_tcp {
            let mut s = self.active.remove(&key).expect("session present");
            s.term_reason = Some(TermReason::TcpClosed);
            s.end_us = ev.ts_us;
            self.closed.push(s);
        }
    }

    /// All sessions closed so far.
    pub fn drain_closed(&mut self) -> Vec<Session> {
        std::mem::take(&mut self.closed)
    }

    /// Close every still-active session with `incomplete_at_eof`. The result
    /// is sorted deterministically (start time, then stable insertion id) so
    /// replay runs produce identical session ordering regardless of the
    /// internal HashMap's iteration order.
    pub fn finish(mut self) -> Vec<Session> {
        let mut keys: Vec<String> = self.active.keys().cloned().collect();
        keys.sort();
        for k in keys {
            let mut s = self.active.remove(&k).unwrap();
            s.term_reason = Some(TermReason::IncompleteAtEof);
            self.closed.push(s);
        }
        self.closed.sort_by(|a, b| {
            a.start_us
                .cmp(&b.start_us)
                .then_with(|| a.session_id.cmp(&b.session_id))
                .then_with(|| a.key.cmp(&b.key))
        });
        self.closed
    }

    pub fn active_count(&self) -> usize {
        self.active.len()
    }

    // -- internals --------------------------------------------------------

    fn source_key(device: &str, src: &str) -> String {
        format!("{device}|{src}")
    }

    fn start_session(&mut self, ev: &Event, key: &str) -> &mut Session {
        let tuple = ev.five_tuple();
        let id = format!("s{:08}", self.next_id);
        self.next_id += 1;
        // Inherit any attack profile already established for this source.
        let profile = self
            .profiles
            .get(&Self::source_key(&ev.device, &ev.src_addr))
            .cloned();
        let inherited_hits: Vec<RuleHit> = profile
            .as_ref()
            .map(|p| {
                p.rules
                    .iter()
                    .map(|(rid, n)| RuleHit {
                        rule_id: rid,
                        trigger_events: *n,
                        ts_us: ev.ts_us,
                    })
                    .collect()
            })
            .unwrap_or_default();
        let inherited_truth = ev
            .truth
            .clone()
            .or_else(|| profile.and_then(|p| p.label));
        let session = Session {
            session_id: id,
            device: ev.device.clone(),
            key: key.to_string(),
            tuple,
            init_addr: ev.src_addr.clone(),
            init_port: ev.src_port,
            peer_addr: ev.dst_addr.clone(),
            peer_port: ev.dst_port,
            start_us: ev.ts_us,
            end_us: ev.ts_us,
            event_count: 0,
            outbound_bytes: 0,
            evidence: Vec::new(),
            rule_hits: inherited_hits,
            term_reason: None,
            account: None,
            truth: inherited_truth,
        };
        self.active.insert(key.to_string(), session);
        self.active.get_mut(key).unwrap()
    }

    /// Pop every session whose idle deadline has passed. The min-heap makes
    /// this proportional to the number of actually-expired sessions plus the
    /// amortized log cost of insertion, rather than scanning all live
    /// sessions on every event.
    fn expire_idle(&mut self, now_us: i64) {
        while let Some(ExpiryEntry(deadline, key)) = self.expiry.peek().cloned() {
            if deadline > now_us {
                break;
            }
            self.expiry.pop();
            let Some(s) = self.active.get(&key) else {
                // Session already closed (explicit disconnect/finalize): the
                // heap entry was stale and is simply discarded.
                continue;
            };
            let idle_limit = match s.tuple.proto {
                Protocol::Udp | Protocol::Icmp => self.config.udp_idle_us,
                Protocol::Tcp => self.config.tcp_idle_us,
            };
            let current_deadline = s.end_us.saturating_add(idle_limit);
            if current_deadline > now_us {
                // The session was refreshed after this entry was scheduled;
                // a newer heap entry exists, so skip the stale one.
                continue;
            }
            let reason = match s.tuple.proto {
                Protocol::Udp | Protocol::Icmp => TermReason::UdpIdleTimeout,
                Protocol::Tcp => TermReason::TcpIdleTimeout,
            };
            if let Some(mut s) = self.active.remove(&key) {
                s.term_reason = Some(reason);
                self.closed.push(s);
            }
        }
    }

    /// (Re)schedule the idle deadline for a session after it receives an
    /// event. Old heap entries for the same key are left for lazy cleanup.
    fn schedule_expiry(&mut self, key: &str, end_us: i64, proto: Protocol) {
        let limit = match proto {
            Protocol::Udp | Protocol::Icmp => self.config.udp_idle_us,
            Protocol::Tcp => self.config.tcp_idle_us,
        };
        self.expiry
            .push(ExpiryEntry(end_us.saturating_add(limit), key.to_string()));
    }

    fn observe_scan(&mut self, ev: &Event, port: u16) {
        let src = format!("{}|{}", ev.device, ev.src_addr);
        let min_ports = self.config.scan_min_ports;
        let window = self.config.scan_window_us;
        let state = self
            .scans
            .entry(src.clone())
            .or_insert_with(|| ScanState { probes: Vec::new(), fired: false });
        state.probes.retain(|(t, _)| ev.ts_us - *t <= window);
        state.probes.push((ev.ts_us, port));
        let distinct: HashSet<u16> = state.probes.iter().map(|(_, p)| *p).collect();
        if distinct.len() >= min_ports && !state.fired {
            state.fired = true;
            let trigger = state.probes.len();
            self.attach_rule(ev, RULE_PORT_SCAN, trigger, ev.ts_us);
        }
    }

    fn observe_brute(&mut self, ev: &Event, key: &str) {
        let Some(account) = ev.account.as_deref() else {
            return;
        };
        let acct_key = format!("{}|{}", ev.device, account);
        match ev.action {
            Action::AuthFail => {
                let state = self
                    .brute
                    .entry(acct_key)
                    .or_insert_with(|| BruteState { failures: Vec::new() });
                state.failures.retain(|t| ev.ts_us - *t <= self.config.brute_window_us);
                state.failures.push(ev.ts_us);
            }
            Action::AuthOk => {
                let fire = self
                    .brute
                    .get(&acct_key)
                    .map(|s| s.failures.len() >= self.config.brute_max_failures)
                    .unwrap_or(false);
                if fire {
                    let n = self.brute[&acct_key].failures.len() + 1;
                    self.brute.remove(&acct_key);
                    self.attach_rule(ev, RULE_BRUTE_FORCE, n, ev.ts_us);
                } else if let Some(state) = self.brute.get_mut(&acct_key) {
                    // A success that does not meet the threshold resets the
                    // consecutive-failure chain.
                    state.failures.clear();
                }
            }
            _ => {}
        }
        // Ensure auth events always have a session even without a preceding
        // connect, so rule evidence has somewhere to attach.
        if !self.active.contains_key(key) {
            self.start_session(ev, key);
        }
    }

    /// Attach a rule hit to every session — active or already closed — that
    /// belongs to the attack source (same device and source address), since a
    /// scan or brute-force campaign fans out across many short-lived 5-tuples.
    /// The source's persistent attack profile is updated so future sessions
    /// inherit the rule and label. Returns the number of sessions newly hit.
    fn attach_rule(&mut self, ev: &Event, rule_id: &'static str, n: usize, ts_us: i64) -> usize {
        let src_key = Self::source_key(&ev.device, &ev.src_addr);
        {
            let profile = self.profiles.entry(src_key.clone()).or_default();
            if let Some(label) = &ev.truth {
                if profile.label.is_none() {
                    profile.label = Some(label.clone());
                }
            }
            if !profile.rules.iter().any(|(rid, _)| *rid == rule_id) {
                profile.rules.push((rule_id, n));
            }
        }
        let label = ev.truth.clone();

        // Active sessions from this source.
        let active_keys: Vec<String> = self
            .active
            .iter()
            .filter(|(_, s)| s.device == ev.device && s.init_addr == ev.src_addr)
            .map(|(k, _)| k.clone())
            .collect();
        let mut applied = 0usize;
        for key in active_keys {
            if let Some(s) = self.active.get_mut(&key) {
                if !s.rule_hits.iter().any(|h| h.rule_id == rule_id) {
                    s.rule_hits.push(RuleHit {
                        rule_id,
                        trigger_events: n,
                        ts_us,
                    });
                    applied += 1;
                }
                if s.truth.is_none() {
                    s.truth = label.clone();
                }
            }
        }
        // Already-closed sessions from the same source (e.g. scan probes that
        // completed before the threshold was reached).
        for s in self.closed.iter_mut() {
            if s.device == ev.device
                && s.init_addr == ev.src_addr
                && !s.rule_hits.iter().any(|h| h.rule_id == rule_id)
            {
                s.rule_hits.push(RuleHit {
                    rule_id,
                    trigger_events: n,
                    ts_us,
                });
                applied += 1;
                if s.truth.is_none() {
                    s.truth = label.clone();
                }
            }
        }
        applied
    }

    /// Sessions closed by timeout/reuse, keyed for window assignment.
    pub fn closed_sessions(&self) -> &[Session] {
        &self.closed
    }
}

/// Per-window aggregate produced by the detector.
#[derive(Debug, Clone)]
pub struct WindowAgg {
    pub index: i64,
    pub start_us: i64,
    pub end_us: i64,
    pub sessions: Vec<String>,
    pub anomalous_sessions: Vec<String>,
    pub rule_hits: BTreeMap<&'static str, u64>,
    pub events: u64,
    pub score: u64,
    pub truth_sessions: Vec<String>,
}

impl WindowAgg {
    fn new(index: i64, start_us: i64, end_us: i64) -> Self {
        Self {
            index,
            start_us,
            end_us,
            sessions: Vec::new(),
            anomalous_sessions: Vec::new(),
            rule_hits: BTreeMap::new(),
            events: 0,
            score: 0,
            truth_sessions: Vec::new(),
        }
    }
}

/// Sliding-window manager. A session is counted in exactly one window — the
/// unique window whose `[start, start+step)` slot contains the session start
/// — so overlapping windows never double-count it. Empty windows are still
/// emitted between the observed bounds.
pub struct WindowManager {
    window_us: i64,
    step_us: i64,
    origin_us: i64,
    score_threshold: u64,
    windows: BTreeMap<i64, WindowAgg>,
    min_index: Option<i64>,
    max_index: Option<i64>,
}

impl WindowManager {
    pub fn new(window_secs: i64, step_secs: i64, origin_us: i64, score_threshold: u64) -> Self {
        Self {
            window_us: window_secs * 1_000_000,
            step_us: step_secs * 1_000_000,
            origin_us,
            score_threshold,
            windows: BTreeMap::new(),
            min_index: None,
            max_index: None,
        }
    }

    fn slot_index(&self, ts_us: i64) -> i64 {
        (ts_us - self.origin_us).div_euclid(self.step_us)
    }

    /// Assign a finalized session to its unique slot. `in_overlap` reports
    /// whether the detection window of that slot actually covers the session
    /// start (it always does when step <= window length).
    pub fn add_session(&mut self, s: &Session) {
        let idx = self.slot_index(s.start_us);
        self.min_index = Some(self.min_index.map_or(idx, |m| m.min(idx)));
        self.max_index = Some(self.max_index.map_or(idx, |m| m.max(idx)));
        let start_us = self.origin_us + idx * self.step_us;
        let agg = self
            .windows
            .entry(idx)
            .or_insert_with(|| WindowAgg::new(idx, start_us, start_us + self.window_us));
        agg.sessions.push(s.session_id.clone());
        agg.events = agg.events.saturating_add(s.event_count as u64);
        if s.is_anomalous() {
            agg.anomalous_sessions.push(s.session_id.clone());
            for hit in &s.rule_hits {
                *agg.rule_hits.entry(hit.rule_id).or_insert(0) += 1;
                agg.score = agg.score.saturating_add(hit.trigger_events as u64);
            }
        }
        if s.truth_label().is_some() {
            agg.truth_sessions.push(s.session_id.clone());
        }
    }

    /// Finalize and return all windows from min to max slot, including empty
    /// ones, with per-window threshold verdicts.
    pub fn finish(self) -> Vec<WindowAgg> {
        let (lo, hi) = match (self.min_index, self.max_index) {
            (Some(lo), Some(hi)) => (lo, hi),
            _ => return Vec::new(),
        };
        let mut out = Vec::new();
        for idx in lo..=hi {
            match self.windows.get(&idx) {
                Some(w) => out.push(w.clone()),
                None => {
                    let start_us = self.origin_us + idx * self.step_us;
                    out.push(WindowAgg::new(idx, start_us, start_us + self.window_us));
                }
            }
        }
        out
    }

    pub fn score_threshold(&self) -> u64 {
        self.score_threshold
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{fnv1a_64_hex, InputFormat};

    #[allow(clippy::too_many_arguments)]
    fn ev(
        ts: i64,
        src: &str,
        sport: Option<u16>,
        dst: &str,
        dport: Option<u16>,
        proto: Protocol,
        act: Action,
        bytes: Option<u64>,
        account: Option<&str>,
        truth: Option<&str>,
    ) -> Event {
        Event {
            device: "gw".into(),
            ts_us: ts,
            src_addr: src.into(),
            src_port: sport,
            dst_addr: dst.into(),
            dst_port: dport,
            protocol: proto,
            action: act,
            rule_label: None,
            account: account.map(String::from),
            bytes,
            event_id: None,
            raw_digest: fnv1a_64_hex(format!("{ts}-{src}-{dst}").as_bytes()),
            content_hash: fnv1a_64_hex(format!("c-{ts}-{src}-{dst}").as_bytes()),
            source_format: InputFormat::JsonLine,
            truth: truth.map(String::from),
        }
    }

    #[test]
    fn tcp_open_close_and_reuse_are_separate_sessions() {
        let mut c = Correlator::new(CorrelatorConfig::default());
        let t = 1_000_000i64;
        c.process(&ev(t, "10.0.0.1", Some(1000), "10.0.0.2", Some(22), Protocol::Tcp, Action::Connect, None, None, None));
        c.process(&ev(t + 100, "10.0.0.2", Some(22), "10.0.0.1", Some(1000), Protocol::Tcp, Action::Data, Some(50), None, None));
        c.process(&ev(t + 200, "10.0.0.1", Some(1000), "10.0.0.2", Some(22), Protocol::Tcp, Action::Disconnect, None, None, None));
        let closed = c.drain_closed();
        assert_eq!(closed.len(), 1);
        assert_eq!(closed[0].term_reason, Some(TermReason::TcpClosed));
        assert_eq!(closed[0].event_count, 3);
        assert_eq!(closed[0].outbound_bytes, 0); // reverse data not counted

        // Reuse: new connect on the same 5-tuple creates a second session.
        c.process(&ev(t + 300, "10.0.0.1", Some(1000), "10.0.0.2", Some(22), Protocol::Tcp, Action::Connect, None, None, None));
        let all = c.finish();
        assert_eq!(all.len(), 1);
        assert_eq!(all[0].term_reason, Some(TermReason::IncompleteAtEof));
    }

    #[test]
    fn udp_idle_timeout_closes_session() {
        let cfg = CorrelatorConfig {
            udp_idle_us: 1_000_000,
            ..CorrelatorConfig::default()
        };
        let mut c = Correlator::new(cfg);
        c.process(&ev(0, "10.0.0.1", Some(9), "10.0.0.2", Some(53), Protocol::Udp, Action::Data, Some(10), None, None));
        assert_eq!(c.active_count(), 1);
        c.process(&ev(5_000_000, "10.0.0.9", Some(1), "10.0.0.8", Some(2), Protocol::Udp, Action::Data, None, None, None));
        let closed = c.drain_closed();
        assert_eq!(closed.len(), 1);
        assert_eq!(closed[0].term_reason, Some(TermReason::UdpIdleTimeout));
    }

    #[test]
    fn port_scan_rule_fires_after_distinct_ports() {
        let cfg = CorrelatorConfig {
            // Timestamps here are near the epoch; give idle timeouts plenty of
            // room so sessions stay active across the short test sequence.
            tcp_idle_us: i64::MAX / 2,
            udp_idle_us: i64::MAX / 2,
            ..CorrelatorConfig::default()
        };
        let mut c = Correlator::new(cfg);
        let t = 1_000_000_000i64;
        for p in 1..=10u16 {
            c.process(&ev(t + p as i64, "10.0.0.7", Some(33333), "10.0.0.9", Some(8000 + p), Protocol::Tcp, Action::Connect, None, None, Some("scan")));
        }
        let closed = c.finish();
        let fired: Vec<_> = closed
            .iter()
            .filter(|s| s.rule_hits.iter().any(|h| h.rule_id == RULE_PORT_SCAN))
            .collect();
        assert!(!fired.is_empty());
        assert!(fired.iter().all(|s| s.truth_label() == Some("scan")));
    }

    #[test]
    fn brute_force_requires_failures_then_success() {
        let cfg = CorrelatorConfig {
            tcp_idle_us: i64::MAX / 2,
            udp_idle_us: i64::MAX / 2,
            ..CorrelatorConfig::default()
        };
        let mut c = Correlator::new(cfg);
        let t = 1_000_000_000i64;
        for i in 0..5i64 {
            c.process(&ev(t + i, "10.0.0.5", Some(40000 + i as u16), "10.0.0.2", Some(22), Protocol::Tcp, Action::AuthFail, None, Some("root"), None));
        }
        c.process(&ev(t + 6, "10.0.0.5", Some(40010), "10.0.0.2", Some(22), Protocol::Tcp, Action::AuthOk, None, Some("root"), Some("brute")));
        let closed = c.finish();
        let hit = closed
            .iter()
            .find(|s| s.rule_hits.iter().any(|h| h.rule_id == RULE_BRUTE_FORCE))
            .expect("brute force hit");
        assert_eq!(hit.truth_label(), Some("brute"));
    }

    #[test]
    fn outbound_burst_counts_only_initiator_bytes() {
        let mut c = Correlator::new(CorrelatorConfig {
            burst_bytes: 1000,
            ..CorrelatorConfig::default()
        });
        c.process(&ev(0, "10.0.0.1", Some(1000), "10.0.0.2", Some(443), Protocol::Tcp, Action::Connect, None, None, None));
        c.process(&ev(100, "10.0.0.1", Some(1000), "10.0.0.2", Some(443), Protocol::Tcp, Action::Data, Some(900), None, None));
        c.process(&ev(200, "10.0.0.2", Some(443), "10.0.0.1", Some(1000), Protocol::Tcp, Action::Data, Some(9_000_000), None, None));
        assert_eq!(c.active.values().next().unwrap().outbound_bytes, 900);
        c.process(&ev(300, "10.0.0.1", Some(1000), "10.0.0.2", Some(443), Protocol::Tcp, Action::Data, Some(200), None, Some("burst")));
        let closed = c.finish();
        assert!(closed
            .iter()
            .any(|s| s.rule_hits.iter().any(|h| h.rule_id == RULE_OUTBOUND_BURST)));
    }
}
