//! Terminal reporting: deterministic, reviewable quantitative summaries.
//!
//! Output contains only counters, ratios and short identifiers/digests —
//! never credential material or full raw log lines.

use crate::correlate::{
    RULE_BRUTE_FORCE, RULE_OUTBOUND_BURST, RULE_PORT_SCAN,
};
use crate::PipelineResult;
use crate::time::format_rfc3339_us;
use std::fmt::Write as _;

pub fn render(result: &PipelineResult) -> String {
    let mut out = String::with_capacity(4096);
    let s = &result.stats;
    let d = &result.detection;
    let e = &d.evaluation;

    let _ = writeln!(out, "edgewatch run summary");
    let _ = writeln!(out, "====================");
    // Required acceptance fields — stable key names, non-negative integers
    // and three-decimal ratios.
    let _ = writeln!(out, "processed_events={}", s.processed_events);
    let _ = writeln!(out, "invalid_lines={}", s.invalid_lines);
    let _ = writeln!(out, "late_events={}", s.late_events);
    let _ = writeln!(out, "duplicate_events={}", s.duplicate_events);
    let _ = writeln!(out, "windows={}", d.counters.windows);
    let _ = writeln!(out, "anomalous_sessions={}", d.counters.anomalous_sessions);
    let _ = writeln!(out, "rule_hits={}", d.counters.rule_hits);
    let _ = writeln!(out, "rule_hit_rate={:.3}", e.rule_hit_rate());
    let _ = writeln!(out, "false_positives={}", e.false_positives);
    let _ = writeln!(out, "false_negatives={}", e.false_negatives);

    let _ = writeln!(out, "---- pipeline ----");
    let _ = writeln!(out, "total_lines={}", s.total_lines);
    let _ = writeln!(out, "inserted_events={}", s.inserted_events);
    let _ = writeln!(out, "batches={}", s.batches);
    let _ = writeln!(out, "failed_batches={}", s.failed_batches);
    let _ = writeln!(out, "clock_wraps={}", s.clock_wraps);
    let _ = writeln!(out, "sessions_total={}", d.counters.sessions);
    let _ = writeln!(out, "flagged_windows={}", d.counters.flagged_windows);
    let _ = writeln!(out, "empty_windows={}", d.counters.empty_windows);
    let _ = writeln!(
        out,
        "elapsed_ms={}",
        result.elapsed.as_millis()
    );
    let _ = writeln!(
        out,
        "throughput_lines_per_sec={:.0}",
        if result.elapsed.as_secs_f64() > 0.0 {
            s.total_lines as f64 / result.elapsed.as_secs_f64()
        } else {
            0.0
        }
    );

    let _ = writeln!(out, "---- rule breakdown ----");
    for rule in [RULE_PORT_SCAN, RULE_BRUTE_FORCE, RULE_OUTBOUND_BURST] {
        let n = d.per_rule_hits.get(rule).copied().unwrap_or(0);
        let _ = writeln!(out, "{rule}={n}");
    }

    let _ = writeln!(out, "---- evaluation ----");
    let _ = writeln!(out, "truth_sessions={}", e.truth_sessions);
    let _ = writeln!(out, "true_positives={}", e.true_positives);
    let _ = writeln!(out, "false_positives={}", e.false_positives);
    let _ = writeln!(out, "false_negatives={}", e.false_negatives);
    let _ = writeln!(out, "rule_hit_rate={:.3}", e.rule_hit_rate());

    if !result.invalid_breakdown.is_empty() {
        let _ = writeln!(out, "---- invalid lines by reason ----");
        for (reason, n) in &result.invalid_breakdown {
            let _ = writeln!(out, "{reason}={n}");
        }
    }
    if !result.wraps_by_device.is_empty() {
        let _ = writeln!(out, "---- clock wraps by device ----");
        for (device, n) in &result.wraps_by_device {
            let _ = writeln!(out, "{device}={n}");
        }
    }

    let _ = writeln!(out, "---- windows ----");
    for w in &d.windows {
        let verdict = if w.score >= d.score_threshold {
            "ANOMALY"
        } else {
            "ok"
        };
        let _ = writeln!(
            out,
            "window idx={} start={} end={} sessions={} anomalous={} events={} score={} verdict={}",
            w.index,
            format_rfc3339_us(w.start_us),
            format_rfc3339_us(w.end_us),
            w.sessions.len(),
            w.anomalous_sessions.len(),
            w.events,
            w.score,
            verdict,
        );
    }

    let _ = writeln!(out, "---- anomalous sessions (evidence digests only) ----");
    // Deterministic ordering: sessions originate from a HashMap, so sort the
    // anomalous subset by start time, session id and 5-tuple key for replay.
    let mut anomalous: Vec<&crate::correlate::Session> =
        result.sessions.iter().filter(|s| s.is_anomalous()).collect();
    anomalous.sort_by(|a, b| {
        a.start_us
            .cmp(&b.start_us)
            .then_with(|| a.session_id.cmp(&b.session_id))
            .then_with(|| a.key.cmp(&b.key))
    });
    for sess in anomalous {
        let rules: Vec<String> = sess
            .rule_hits
            .iter()
            .map(|h| format!("{}(events={})", h.rule_id, h.trigger_events))
            .collect();
        let _ = writeln!(
            out,
            "session={} device={} init={}:{} peer={}:{} start={} end={} reason={} truth={} hits=[{}]",
            sess.session_id,
            sess.device,
            sess.init_addr,
            sess.init_port.map(|p| p.to_string()).unwrap_or_else(|| "-".into()),
            sess.peer_addr,
            sess.peer_port.map(|p| p.to_string()).unwrap_or_else(|| "-".into()),
            format_rfc3339_us(sess.start_us),
            format_rfc3339_us(sess.end_us),
            sess.term_reason.as_ref().map(|r| r.as_str()).unwrap_or("open"),
            sess.truth_label().unwrap_or("-"),
            rules.join(",")
        );
    }
    out
}
