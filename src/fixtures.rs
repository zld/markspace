//! Deterministic synthetic-fixture generation.
//!
//! All data is produced in-process — no external files, no network and no
//! real targets. The generator mixes benign background traffic, three known
//! attack sequences, out-of-order and late events, a device clock wrap, a
//! cross-midnight span, duplicated lines and corrupted lines. Ground-truth
//! labels travel in a dedicated `truth` field that the detector never reads.

use crate::ingest::parser::parse_line;
use crate::model::{Action, Protocol};
use crate::time::format_rfc3339_us;

/// Fixed origin: 2026-11-14T23:54:00Z. Background traffic reaches ~00:02 and
/// the attack/OOO/wrap blocks extend to ~00:06 on 2026-11-15, so the stream
/// genuinely crosses midnight UTC while keeping every block ordered.
pub const ORIGIN_US: i64 = 1_794_700_440_000_000;
const SEC: i64 = 1_000_000;

/// Deterministic xorshift64* RNG so repeated runs yield identical fixtures.
#[derive(Debug, Clone)]
pub struct Rng(u64);

impl Rng {
    pub fn new(seed: u64) -> Self {
        Rng(seed.max(1))
    }

    pub fn next_u64(&mut self) -> u64 {
        let mut x = self.0;
        x ^= x >> 12;
        x ^= x << 25;
        x ^= x >> 27;
        self.0 = x;
        x.wrapping_mul(0x2545f4914f6cdd1d)
    }

    pub fn range(&mut self, lo: u64, hi: u64) -> u64 {
        lo + self.next_u64() % (hi - lo + 1)
    }
}

/// One generated input line plus bookkeeping describing what it represents.
#[derive(Debug, Clone)]
pub struct GeneratedLine {
    pub line: String,
    /// True when the line is intentionally corrupt/unparseable.
    pub invalid: bool,
    /// True for duplicate copies of an earlier valid line.
    pub duplicate: bool,
    /// True when emitted beyond the lateness tolerance.
    pub late: bool,
    /// True when part of the clock-wrap block.
    pub wrapped: bool,
    /// Ground-truth anomaly label, if any.
    pub truth: Option<String>,
}

/// Expected counts for offline verification.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct FixtureExpectations {
    pub total_lines: u64,
    pub valid_unique_events: u64,
    pub invalid_lines: u64,
    pub duplicate_lines: u64,
    pub late_events: u64,
    pub clock_wraps: u64,
    /// Number of distinct labelled attack campaigns (one per rule type).
    pub labelled_sessions: u64,
    /// The distinct ground-truth labels embedded in the feed.
    pub labels: Vec<String>,
}

/// Parameters shared with the runtime so fixtures exercise real thresholds.
#[derive(Debug, Clone)]
pub struct FixtureProfile {
    pub lateness_secs: i64,
    pub scan_min_ports: usize,
    pub brute_failures: usize,
    pub burst_bytes: u64,
}

impl Default for FixtureProfile {
    fn default() -> Self {
        Self {
            lateness_secs: 15,
            scan_min_ports: 10,
            brute_failures: 5,
            burst_bytes: 1_000_000,
        }
    }
}

#[derive(Debug, Clone)]
struct JsonBuilder {
    device: String,
    ts_us: i64,
    src: String,
    sport: u16,
    dst: String,
    dport: Option<u16>,
    proto: Protocol,
    act: Action,
    bytes: Option<u64>,
    account: Option<String>,
    eid: String,
    truth: Option<String>,
}

impl JsonBuilder {
    fn line(&self) -> String {
        let mut s = String::from("{");
        s.push_str(&format!("\"device\":\"{}\",", self.device));
        s.push_str(&format!("\"ts\":\"{}\",", format_rfc3339_us(self.ts_us)));
        s.push_str(&format!("\"src_addr\":\"{}\",", self.src));
        s.push_str(&format!("\"src_port\":{},", self.sport));
        s.push_str(&format!("\"dst_addr\":\"{}\",", self.dst));
        match self.dport {
            Some(p) => s.push_str(&format!("\"dst_port\":{p},")),
            None => s.push_str("\"dst_port\":null,"),
        }
        s.push_str(&format!("\"protocol\":\"{}\",", self.proto.as_str()));
        s.push_str(&format!("\"action\":\"{}\",", self.act.as_str()));
        match &self.bytes {
            Some(b) => s.push_str(&format!("\"bytes\":{b},")),
            None => s.push_str("\"bytes\":null,"),
        }
        match &self.account {
            Some(a) => s.push_str(&format!("\"account\":\"{a}\",")),
            None => s.push_str("\"account\":null,"),
        }
        s.push_str(&format!("\"event_id\":\"{}\"", self.eid));
        if let Some(t) = &self.truth {
            s.push_str(&format!(",\"truth\":\"{t}\""));
        }
        s.push('}');
        s
    }
}

#[allow(clippy::too_many_arguments)]
fn syslog_line(
    device: &str,
    ts_us: i64,
    src: &str,
    sport: u16,
    dst: &str,
    dport: u16,
    proto: Protocol,
    act: Action,
    eid: &str,
    truth: Option<&str>,
) -> String {
    // Convert the UTC microsecond into the legacy `MMM dd hh:mm:ss` shape
    // (UTC, year supplied by parser config) to exercise the second parser.
    let formatted = format_rfc3339_us(ts_us);
    let date = &formatted[..10];
    let time = &formatted[11..19];
    let month = match &date[5..7] {
        "01" => "Jan",
        "02" => "Feb",
        "03" => "Mar",
        "04" => "Apr",
        "05" => "May",
        "06" => "Jun",
        "07" => "Jul",
        "08" => "Aug",
        "09" => "Sep",
        "10" => "Oct",
        "11" => "Nov",
        _ => "Dec",
    };
    let day = &date[8..10];
    let mut s = format!(
        "<134>{month} {day} {time} {device} edgewatch[4242]: proto={} src={src} sport={sport} dst={dst} dport={dport} act={} eid={eid}",
        proto.as_str(),
        act.as_str()
    );
    if let Some(t) = truth {
        s.push_str(&format!(" truth={t}"));
    }
    s
}

/// Generate the full demo fixture and its expectations.
pub fn generate_demo(profile: &FixtureProfile) -> (Vec<GeneratedLine>, FixtureExpectations) {
    let mut rng = Rng::new(edge_seed());
    let mut lines: Vec<GeneratedLine> = Vec::new();
    let devices = ["gw-edge-1", "gw-edge-2", "gw-edge-3"];
    let mut eid_counter = 0u64;
    let mut next_eid = move || {
        eid_counter += 1;
        format!("evt-{eid_counter:06}")
    };
    let mut labelled_sessions = 0u64;

    #[allow(unused_mut)]
    let mut push_json = |lines: &mut Vec<GeneratedLine>, b: JsonBuilder| {
        let truth = b.truth.clone();
        lines.push(GeneratedLine {
            line: b.line(),
            invalid: false,
            duplicate: false,
            late: false,
            wrapped: false,
            truth,
        });
    };

    // ---- Benign background: many short TCP/UDP sessions across 8 minutes.
    // Per-device timestamps advance monotonically with bounded jitter so the
    // inter-arrival gap stays well inside the lateness tolerance; the global
    // stream still interleaves devices out of order.
    let span_secs = 480i64;
    let mut dev_last: [i64; 3] = [ORIGIN_US; 3];
    for t in 0..span_secs {
        // ~8 benign events per second, round-robin across devices. Each
        // device clock advances strictly by a small positive gap (never
        // reset to the wall base, which would itself be a backward jump), so
        // benign traffic stays within the lateness tolerance while the global
        // stream still interleaves devices.
        for j in 0..8u64 {
            let di = (j % 3) as usize;
            let dev = devices[di];
            let _base = ORIGIN_US + t * SEC;
            let gap = rng.range(80_000, 300_000) as i64;
            let ts = dev_last[di] + gap;
            dev_last[di] = ts;
            let src = format!("10.10.{}.{}", rng.range(0, 3), rng.range(2, 250));
            let dst = format!("10.20.{}.{}", rng.range(0, 3), rng.range(2, 250));
            let sport = rng.range(20000, 60000) as u16;
            let dport = [80u16, 443, 53, 8080][(rng.next_u64() as usize) % 4];
            let proto = if rng.next_u64().is_multiple_of(4) { Protocol::Udp } else { Protocol::Tcp };
            let eid = next_eid();
            push_json(
                &mut lines,
                JsonBuilder {
                    device: dev.into(),
                    ts_us: ts,
                    src: src.clone(),
                    sport,
                    dst: dst.clone(),
                    dport: Some(dport),
                    proto,
                    act: Action::Connect,
                    bytes: None,
                    account: None,
                    eid: eid.clone(),
                    truth: None,
                },
            );
            if proto == Protocol::Tcp {
                // Follow-up events strictly follow the connect on the same
                // device clock, and advance the per-device watermark so no
                // benign event lands behind its tolerance window.
                let data_ts = (ts + rng.range(1_000, 50_000) as i64).max(dev_last[di] + 1_000);
                // close shortly after, possibly reversed data first
                if rng.next_u64().is_multiple_of(2) {
                    dev_last[di] = data_ts;
                    push_json(
                        &mut lines,
                        JsonBuilder {
                            device: dev.into(),
                            ts_us: data_ts,
                            src: dst.clone(),
                            sport: dport,
                            dst: src.clone(),
                            dport: Some(sport),
                            proto,
                            act: Action::Data,
                            bytes: Some(rng.range(100, 5000)),
                            account: None,
                            eid: next_eid(),
                            truth: None,
                        },
                    );
                }
                let close_ts = (ts + rng.range(50_000, 200_000) as i64).max(dev_last[di] + 1_000);
                dev_last[di] = close_ts;
                push_json(
                    &mut lines,
                    JsonBuilder {
                        device: dev.into(),
                        ts_us: close_ts,
                        src,
                        sport,
                        dst,
                        dport: Some(dport),
                        proto,
                        act: Action::Disconnect,
                        bytes: None,
                        account: None,
                        eid: next_eid(),
                        truth: None,
                    },
                );
            }
        }
    }

    // ---- Attack 1: port scan (RULE_PORT_SCAN), ~40s into the stream.
    let scan_start = ORIGIN_US + 560 * SEC;
    let scanner = "10.10.9.9";
    let scan_target = "10.20.0.50";
    labelled_sessions += 1;
    for i in 0..(profile.scan_min_ports + 6) as u16 {
        push_json(
            &mut lines,
            JsonBuilder {
                device: "gw-edge-1".into(),
                ts_us: scan_start + i as i64 * 400_000,
                src: scanner.into(),
                sport: 51000 + i,
                dst: scan_target.into(),
                dport: Some(9000 + i),
                proto: Protocol::Tcp,
                act: Action::Connect,
                bytes: None,
                account: None,
                eid: next_eid(),
                truth: Some("scan".into()),
            },
        );
    }

    // ---- Attack 2: brute force (RULE_BRUTE_FORCE), ~120s.
    let brute_start = ORIGIN_US + 630 * SEC;
    let brute_src = "10.10.4.4";
    labelled_sessions += 1;
    for i in 0..profile.brute_failures {
        push_json(
            &mut lines,
            JsonBuilder {
                device: "gw-edge-2".into(),
                ts_us: brute_start + i as i64 * SEC,
                src: brute_src.into(),
                sport: 44000 + i as u16,
                dst: "10.20.0.22".into(),
                dport: Some(22),
                proto: Protocol::Tcp,
                act: Action::AuthFail,
                bytes: None,
                account: Some("svc-admin".into()),
                eid: next_eid(),
                truth: Some("brute".into()),
            },
        );
    }
    push_json(
        &mut lines,
        JsonBuilder {
            device: "gw-edge-2".into(),
            ts_us: brute_start + profile.brute_failures as i64 * SEC + 500_000,
            src: brute_src.into(),
            sport: 44100,
            dst: "10.20.0.22".into(),
            dport: Some(22),
            proto: Protocol::Tcp,
            act: Action::AuthOk,
            bytes: None,
            account: Some("svc-admin".into()),
            eid: next_eid(),
            truth: Some("brute".into()),
        },
    );

    // ---- Attack 3: outbound byte burst (RULE_OUTBOUND_BURST), ~200s.
    let burst_start = ORIGIN_US + 710 * SEC;
    labelled_sessions += 1;
    let (b_src, b_dst, b_sport) = ("10.10.6.6", "10.20.7.7", 55555u16);
    push_json(
        &mut lines,
        JsonBuilder {
            device: "gw-edge-3".into(),
            ts_us: burst_start,
            src: b_src.into(),
            sport: b_sport,
            dst: b_dst.into(),
            dport: Some(443),
            proto: Protocol::Tcp,
            act: Action::Connect,
            bytes: None,
            account: None,
            eid: next_eid(),
            truth: Some("burst".into()),
        },
    );
    // Three large data events comfortably over the burst threshold.
    let chunk = profile.burst_bytes as i64 / 2 + 50_000;
    for i in 0..3 {
        push_json(
            &mut lines,
            JsonBuilder {
                device: "gw-edge-3".into(),
                ts_us: burst_start + (i + 1) as i64 * SEC,
                src: b_src.into(),
                sport: b_sport,
                dst: b_dst.into(),
                dport: Some(443),
                proto: Protocol::Tcp,
                act: Action::Data,
                bytes: Some(chunk as u64),
                account: None,
                eid: next_eid(),
                truth: Some("burst".into()),
            },
        );
    }
    push_json(
        &mut lines,
        JsonBuilder {
            device: "gw-edge-3".into(),
            ts_us: burst_start + 5 * SEC,
            src: b_src.into(),
            sport: b_sport,
            dst: b_dst.into(),
            dport: Some(443),
            proto: Protocol::Tcp,
            act: Action::Disconnect,
            bytes: None,
            account: None,
            eid: next_eid(),
            truth: Some("burst".into()),
        },
    );

    // ---- A syslog-formatted benign session to exercise parser #2. Placed on
    // the tail timeline so it is not mistaken for a late event.
    let sl_start = ORIGIN_US + 495 * SEC;
    lines.push(GeneratedLine {
        line: syslog_line(
            "gw-edge-1",
            sl_start,
            "10.10.1.11",
            47000,
            "10.20.1.11",
            53,
            Protocol::Udp,
            Action::Data,
            &next_eid(),
            None,
        ),
        invalid: false,
        duplicate: false,
        late: false,
        wrapped: false,
        truth: None,
    });

    // ---- Out-of-order events: a dedicated tight sequence placed just after
    // the background stream's end so the block itself is not late. Each
    // adjacent pair shares a timestamp and is emitted reversed; the stable
    // global sort preserves the reversed input order on ties, so the second
    // event of each pair arrives "early" while staying within tolerance.
    // Must sit strictly after every earlier gw-edge-1 block (background ends
    // ~495s, the port scan runs 560-568s), so the device watermark cannot make
    // this in-tolerance reordering look like lateness.
    let tail = ORIGIN_US + 590 * SEC;
    let ooo_base = tail;
    let mut ooo_block: Vec<GeneratedLine> = (0..24u16)
        .map(|i| {
            let b = JsonBuilder {
                device: "gw-edge-1".into(),
                ts_us: ooo_base + (i as i64 / 2) * 300_000,
                src: "10.10.2.20".into(),
                sport: 49000 + i,
                dst: "10.20.2.20".into(),
                dport: Some(443),
                proto: Protocol::Tcp,
                act: if i % 2 == 0 { Action::Connect } else { Action::Data },
                bytes: if i % 2 == 0 { None } else { Some(120) },
                account: None,
                eid: format!("ooo-{i:03}"),
                truth: None,
            };
            GeneratedLine {
                line: b.line(),
                invalid: false,
                duplicate: false,
                late: false,
                wrapped: false,
                truth: None,
            }
        })
        .collect();
    // Reverse each adjacent pair -> the data event precedes its connect while
    // both keep the same timestamp (reordering, not a backward jump).
    let mut i = 0usize;
    while i + 1 < ooo_block.len() {
        ooo_block.swap(i, i + 1);
        i += 2;
    }
    lines.append(&mut ooo_block);

    // Sort all normal lines by normalized timestamp to guarantee append order
    // matches time order — EXCEPT the OOO block, whose internal reversed order
    // must be preserved to genuinely exercise the reorder buffer. We pull the
    // OOO lines out, stable-sort the rest, then merge the OOO block back at its
    // timestamp position with its original (disordered) ordering intact.
    let mut kept: Vec<GeneratedLine> = Vec::with_capacity(lines.len());
    let mut ooo_kept: Vec<GeneratedLine> = Vec::new();
    for g in lines.drain(..) {
        if g.line.contains("\"event_id\":\"ooo-") {
            ooo_kept.push(g);
        } else {
            kept.push(g);
        }
    }
    kept.sort_by(|a, b| {
        let ta = parse_line(&a.line, 0, 0).map(|e| e.ts_us).unwrap_or(i64::MAX);
        let tb = parse_line(&b.line, 0, 0).map(|e| e.ts_us).unwrap_or(i64::MAX);
        ta.cmp(&tb)
    });
    // Find the first sorted line at or after the OOO block's base timestamp
    // and splice the disordered OOO block in there.
    let ooo_start_ts = parse_line(&ooo_kept[0].line, 0, 0).map(|e| e.ts_us).unwrap_or(0);
    let splice_at = kept
        .iter()
        .position(|g| {
            parse_line(&g.line, 0, 0).map(|e| e.ts_us).unwrap_or(i64::MAX) >= ooo_start_ts
        })
        .unwrap_or(kept.len());
    for (n, g) in ooo_kept.into_iter().enumerate() {
        kept.insert(splice_at + n, g);
    }
    lines = kept;

    // ---- Late events: standalone records with timestamps far in the past so
    // they exceed the lateness tolerance once the watermark has advanced.
    let mut late_events = 0u64;
    for i in 0..6i64 {
        let ts = ORIGIN_US + i * SEC; // very old relative to watermark
        lines.push(GeneratedLine {
            line: JsonBuilder {
                device: "gw-edge-2".into(),
                ts_us: ts,
                src: "10.10.8.8".into(),
                sport: 60000 + i as u16,
                dst: "10.20.8.8".into(),
                dport: Some(123),
                proto: Protocol::Udp,
                act: Action::Data,
                bytes: Some(64),
                account: None,
                eid: format!("late-{i:03}"),
                truth: None,
            }
            .line(),
            invalid: false,
            duplicate: false,
            late: true,
            wrapped: false,
            truth: None,
        });
        late_events += 1;
    }

    // ---- Clock wrap block on gw-edge-3: device clock jumps ~2h backwards
    // relative to the current watermark, which exceeds the wrap threshold
    // (3600s) and is corrected by shifting the timeline rather than dropped.
    let wrap_anchor = tail + 30 * SEC;
    let wrapped_ts = wrap_anchor - 7_200 * SEC;
    let mut wrap_block: Vec<GeneratedLine> = (0..4)
        .map(|i| GeneratedLine {
            line: JsonBuilder {
                device: "gw-edge-3".into(),
                ts_us: wrapped_ts + i * SEC,
                src: "10.10.3.3".into(),
                sport: 48000 + i as u16,
                dst: "10.20.3.3".into(),
                dport: Some(443),
                proto: Protocol::Tcp,
                act: if i == 0 { Action::Connect } else { Action::Data },
                bytes: if i == 0 { None } else { Some(200) },
                account: None,
                eid: format!("wrap-{i:03}"),
                truth: None,
            }
            .line(),
            invalid: false,
            duplicate: false,
            late: false,
            wrapped: true,
            truth: None,
        })
        .collect();
    lines.append(&mut wrap_block);

    // ---- Duplicate lines: exact replays inserted immediately after their
    // source line. Because the device watermark has barely advanced, each
    // replay stays inside the lateness tolerance and therefore exercises the
    // dedup path rather than being classified late.
    let mut dup_candidates: Vec<(i64, String)> = lines
        .iter()
        .filter(|l| {
            !l.invalid
                && !l.duplicate
                && !l.late
                && !l.wrapped
                && !l.line.contains("\"event_id\":\"ooo-")
        })
        .filter_map(|l| parse_line(&l.line, 0, 0).ok().map(|e| (e.ts_us, l.line.clone())))
        .collect();
    dup_candidates.sort_by_key(|(ts, _)| *ts);
    let dup_sources: Vec<String> = dup_candidates
        .into_iter()
        .map(|(_, line)| line)
        .rev()
        .take(20)
        .collect();
    let duplicate_lines = dup_sources.len() as u64;
    for d in &dup_sources {
        // Locate the matching source line and splice the replay right after.
        if let Some(pos) = lines.iter().position(|g| g.line == *d) {
            lines.insert(
                pos + 1,
                GeneratedLine {
                    line: d.clone(),
                    invalid: false,
                    duplicate: true,
                    late: false,
                    wrapped: false,
                    truth: None,
                },
            );
        }
    }

    // ---- Corrupt / unparseable lines covering every reason category.
    let corrupt: Vec<(String, bool)> = vec![
        ("".to_string(), false),
        ("   ".to_string(), false),
        ("this line matches no known format at all".to_string(), false),
        (r#"{"device":"gw-x","ts":"2026-11-14T00:00:00Z","src_addr":"300.1.1.1","dst_addr":"10.0.0.1","protocol":"tcp","action":"allow"}"#.to_string(), false),
        (r#"{"device":"gw-x","ts":"2026-11-14T00:00:00Z","src_addr":"10.0.0.1","dst_addr":"10.0.0.2","src_port":99999,"protocol":"tcp","action":"allow"}"#.to_string(), false),
        (r#"{"device":"gw-x","ts":"2026-11-14T00:00:00","src_addr":"10.0.0.1","dst_addr":"10.0.0.2","protocol":"tcp","action":"allow"}"#.to_string(), false),
        (r#"{"device":"gw-x","ts":"2026-11-14T00:00:00Z","src_addr":"10.0.0.1","protocol":"tcp","action":"allow"}"#.to_string(), false),
        (r#"{"device":"gw-x","ts":"2026-11-14T00:00:00Z","src_addr":"10.0.0.1","dst_addr":"10.0.0.2","protocol":"snafu","action":"allow"}"#.to_string(), false),
        (r#"{"device":"gw-x","ts":"not-a-timestamp","src_addr":"10.0.0.1","dst_addr":"10.0.0.2","protocol":"tcp","action":"allow"}"#.to_string(), false),
        (r#"{"device":"gw-x","broken json"#.to_string(), false),
        // A corrupt line that includes credentials: must stay redacted.
        ("Nov 14 10:00:00 gw-x app: garbage password=do-not-leak token=secret123".to_string(), false),
    ];
    let invalid_lines = corrupt.len() as u64;
    for (text, _) in corrupt {
        lines.push(GeneratedLine {
            line: text,
            invalid: true,
            duplicate: false,
            late: false,
            wrapped: false,
            truth: None,
        });
    }

    let total_lines = lines.len() as u64;
    let valid_unique_events = total_lines - invalid_lines - duplicate_lines;

    let expectations = FixtureExpectations {
        total_lines,
        valid_unique_events,
        invalid_lines,
        duplicate_lines,
        late_events,
        clock_wraps: 1,
        labelled_sessions,
        labels: vec!["brute".into(), "burst".into(), "scan".into()],
    };
    (lines, expectations)
}

/// A larger synthetic feed used only for the throughput check.
pub fn generate_benchmark(target_lines: u64) -> Vec<String> {
    let mut rng = Rng::new(99);
    let mut out = Vec::with_capacity(target_lines as usize);
    let mut eid = 0u64;
    while out.len() < target_lines as usize {
        eid += 1;
        let ts = ORIGIN_US + (eid as i64 / 10) * 100_000;
        let b = JsonBuilder {
            device: format!("gw-{}", rng.range(1, 4)),
            ts_us: ts,
            src: format!("10.0.{}.{}", rng.range(0, 20), rng.range(2, 250)),
            sport: rng.range(10000, 60000) as u16,
            dst: format!("10.1.{}.{}", rng.range(0, 20), rng.range(2, 250)),
            dport: Some([80u16, 443, 53][(rng.next_u64() as usize) % 3]),
            proto: Protocol::Tcp,
            act: Action::Connect,
            bytes: None,
            account: None,
            eid: format!("bench-{eid:08}"),
            truth: None,
        };
        out.push(b.line());
    }
    out
}

/// Validate that every non-corrupt generated line parses with the production
/// parser — fixtures and parser share the same implementation by design.
pub fn self_check(lines: &[GeneratedLine]) -> Result<u64, String> {
    let mut parsed = 0u64;
    for (i, g) in lines.iter().enumerate() {
        if g.invalid {
            continue;
        }
        parse_line(&g.line, 0, i as u64 + 1)
            .map(|_| parsed += 1)
            .map_err(|e| format!("fixture line {i} expected to parse but got {}: {e:?}", e.reason.as_str()))?;
    }
    Ok(parsed)
}

// Seed helper kept as a fn so the literal reads as an identifier, not magic.
fn edge_seed() -> u64 {
    0xED6E_77A7_1234_5678
}
