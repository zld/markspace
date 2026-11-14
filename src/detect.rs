//! Sliding-window anomaly scoring, threshold verdicts and the offline
//! false-positive/false-negative evaluation.
//!
//! Ground-truth labels produced by the fixture generator are read *only* in
//! [`DetectionOutcome::evaluate`]. All scoring above works purely on rule
//! hits; labels never influence a score or a verdict.

use crate::correlate::{Session, WindowAgg, WindowManager, RULE_BRUTE_FORCE, RULE_OUTBOUND_BURST, RULE_PORT_SCAN};
use crate::model::Event;

#[derive(Debug, Clone)]
pub struct DetectionConfig {
    pub window_secs: i64,
    pub step_secs: i64,
    /// A window is flagged when its anomaly score reaches this value.
    pub score_threshold: u64,
}

impl Default for DetectionConfig {
    fn default() -> Self {
        Self {
            window_secs: 60,
            step_secs: 60,
            score_threshold: 1,
        }
    }
}

#[derive(Debug, Default, Clone)]
pub struct DetectionCounters {
    pub windows: u64,
    pub flagged_windows: u64,
    pub empty_windows: u64,
    pub sessions: u64,
    pub anomalous_sessions: u64,
    pub rule_hits: u64,
}

#[derive(Debug, Default, Clone)]
pub struct Evaluation {
    /// Labelled sessions (ground-truth anomalies).
    pub truth_sessions: u64,
    /// Detected anomalies that carry no truth label.
    pub false_positives: u64,
    /// Labelled anomalies that no rule fired on.
    pub false_negatives: u64,
    /// Labelled anomalies correctly detected.
    pub true_positives: u64,
}

impl Evaluation {
    /// Rule hit rate over known anomalies: TP / (TP + FN). `0.000` when there
    /// are no labelled anomalies.
    pub fn rule_hit_rate(&self) -> f64 {
        let denom = self.true_positives + self.false_negatives;
        if denom == 0 {
            0.0
        } else {
            self.true_positives as f64 / denom as f64
        }
    }
}

#[derive(Debug, Clone)]
pub struct DetectionOutcome {
    pub counters: DetectionCounters,
    pub evaluation: Evaluation,
    pub per_rule_hits: std::collections::BTreeMap<&'static str, u64>,
    pub windows: Vec<WindowAgg>,
    /// The run-level score threshold used for per-window verdicts.
    pub score_threshold: u64,
}

impl DetectionOutcome {
    /// Assign every finalized session to exactly one sliding-window slot,
    /// include empty windows between the observed bounds, and then evaluate
    /// against ground truth.
    pub fn build(sessions: &[Session], events: &[Event], cfg: &DetectionConfig) -> Self {
        // Origin: earliest event timestamp, so window 0 starts at the first
        // observed microsecond rather than at the Unix epoch.
        let origin = events
            .iter()
            .map(|e| e.ts_us)
            .min()
            .unwrap_or(0);
        let mut manager = WindowManager::new(
            cfg.window_secs,
            cfg.step_secs,
            origin,
            cfg.score_threshold,
        );
        for s in sessions {
            manager.add_session(s);
        }
        let windows = manager.finish();

        let mut counters = DetectionCounters::default();
        let mut per_rule = std::collections::BTreeMap::new();
        for w in &windows {
            counters.windows += 1;
            if w.sessions.is_empty() {
                counters.empty_windows += 1;
            }
            counters.sessions += w.sessions.len() as u64;
            counters.anomalous_sessions += w.anomalous_sessions.len() as u64;
            let window_hits: u64 = w.rule_hits.values().sum();
            counters.rule_hits += window_hits;
            if w.score >= cfg.score_threshold {
                counters.flagged_windows += 1;
            }
            for (rule, n) in &w.rule_hits {
                *per_rule.entry(*rule).or_insert(0u64) += *n;
            }
        }
        // Ensure all known rules appear, even with zero hits.
        for r in [RULE_PORT_SCAN, RULE_BRUTE_FORCE, RULE_OUTBOUND_BURST] {
            per_rule.entry(r).or_insert(0);
        }

        let evaluation = evaluate(sessions);
        DetectionOutcome {
            counters,
            evaluation,
            per_rule_hits: per_rule,
            windows,
            score_threshold: cfg.score_threshold,
        }
    }
}

/// Session-level confusion matrix against the embedded truth labels.
fn evaluate(sessions: &[Session]) -> Evaluation {
    let mut ev = Evaluation::default();
    for s in sessions {
        let labelled = s.truth_label().is_some();
        let detected = s.is_anomalous();
        if labelled {
            ev.truth_sessions += 1;
            if detected {
                ev.true_positives += 1;
            } else {
                ev.false_negatives += 1;
            }
        } else if detected {
            ev.false_positives += 1;
        }
    }
    ev
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::correlate::{RuleHit, Session, TermReason};
    use crate::model::{fnv1a_64_hex, FiveTuple, InputFormat, Protocol};

    fn session(id: &str, start: i64, anomalous: bool, truth: Option<&str>) -> Session {
        let mut s = Session {
            session_id: id.into(),
            device: "gw".into(),
            key: id.into(),
            tuple: FiveTuple {
                device: "gw".into(),
                proto: Protocol::Tcp,
                a_addr: "10.0.0.1".into(),
                a_port: Some(1),
                b_addr: "10.0.0.2".into(),
                b_port: Some(2),
            },
            init_addr: "10.0.0.1".into(),
            init_port: Some(1),
            peer_addr: "10.0.0.2".into(),
            peer_port: Some(2),
            start_us: start,
            end_us: start + 10,
            event_count: 2,
            outbound_bytes: 0,
            evidence: vec![fnv1a_64_hex(id.as_bytes())],
            rule_hits: Vec::new(),
            term_reason: Some(TermReason::TcpClosed),
            account: None,
            truth: truth.map(String::from),
        };
        if anomalous {
            s.rule_hits.push(RuleHit {
                rule_id: RULE_PORT_SCAN,
                trigger_events: 12,
                ts_us: start,
            });
        }
        s
    }

    fn event_at(ts: i64) -> Event {
        Event {
            device: "gw".into(),
            ts_us: ts,
            src_addr: "10.0.0.1".into(),
            src_port: Some(1),
            dst_addr: "10.0.0.2".into(),
            dst_port: Some(2),
            protocol: Protocol::Tcp,
            action: crate::model::Action::Connect,
            rule_label: None,
            account: None,
            bytes: None,
            event_id: None,
            raw_digest: "d".into(),
            content_hash: "c".into(),
            source_format: InputFormat::JsonLine,
            truth: None,
        }
    }

    #[test]
    fn empty_windows_emitted_and_session_counted_once() {
        let sessions = vec![
            session("a", 0, false, None),
            // gap of two full steps, then one session
            session("b", 3 * 60_000_000, true, Some("scan")),
        ];
        let events = vec![event_at(0), event_at(3 * 60_000_000)];
        let out = DetectionOutcome::build(
            &sessions,
            &events,
            &DetectionConfig {
                window_secs: 60,
                step_secs: 60,
                score_threshold: 1,
            },
        );
        assert_eq!(out.counters.windows, 4);
        assert_eq!(out.counters.empty_windows, 2);
        assert_eq!(out.counters.sessions, 2);
        assert_eq!(out.counters.anomalous_sessions, 1);
    }

    #[test]
    fn fp_fn_and_hit_rate() {
        let sessions = vec![
            session("tp", 0, true, Some("scan")),   // detected + labelled
            session("fn", 60_000_000, false, Some("brute")), // labelled, missed
            session("fp", 120_000_000, true, None), // detected, unlabelled
        ];
        let events: Vec<Event> = (0..3).map(|i| event_at(i * 60_000_000)).collect();
        let out = DetectionOutcome::build(&sessions, &events, &DetectionConfig::default());
        assert_eq!(out.evaluation.true_positives, 1);
        assert_eq!(out.evaluation.false_negatives, 1);
        assert_eq!(out.evaluation.false_positives, 1);
        assert!((out.evaluation.rule_hit_rate() - 0.5).abs() < f64::EPSILON);
    }
}
