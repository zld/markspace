//! Integration tests driven by the in-process fixture generator. These verify
//! the whole data flow: parse -> reorder -> transactional dedup -> correlate
//! -> windows -> labelled evaluation.

use edgewatch::correlate::{
    RULE_BRUTE_FORCE, RULE_OUTBOUND_BURST, RULE_PORT_SCAN,
};
use edgewatch::detect::DetectionConfig;
use edgewatch::fixtures::{
    generate_demo, generate_benchmark, self_check, FixtureProfile, ORIGIN_US,
};
use edgewatch::ingest::{read_batches, IngestStats, Source};
use edgewatch::{run, AppConfig};
use std::io::Cursor;
use std::path::PathBuf;

const SEC: i64 = 1_000_000;

fn demo_config(profile: &FixtureProfile) -> AppConfig {
    AppConfig {
        window_secs: 60,
        step_secs: 60,
        lateness_secs: profile.lateness_secs,
        score_threshold: 1,
        batch_size: 2500,
        wrap_threshold_secs: 3600,
        legacy_offset_secs: 0,
        legacy_year: 2026,
        db_path: None,
        correlate: edgewatch::correlate::CorrelatorConfig {
            scan_window_us: 30 * SEC,
            scan_min_ports: profile.scan_min_ports,
            brute_max_failures: profile.brute_failures,
            brute_window_us: 120 * SEC,
            burst_bytes: profile.burst_bytes,
            udp_idle_us: 60 * SEC,
            tcp_idle_us: 300 * SEC,
        },
    }
}

fn write_feed(lines: &[String]) -> PathBuf {
    let dir = std::env::temp_dir();
    let path = dir.join(format!(
        "edgewatch-test-{}-{}.log",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    std::fs::write(&path, lines.join("\n")).unwrap();
    path
}

#[test]
fn all_valid_fixture_lines_parse_with_production_parser() {
    let profile = FixtureProfile::default();
    let (lines, _) = generate_demo(&profile);
    let parsed = self_check(&lines).unwrap();
    let expected_valid = lines.iter().filter(|l| !l.invalid).count() as u64;
    assert_eq!(parsed, expected_valid);
}

#[test]
fn demo_pipeline_reports_required_fields_and_known_attacks() {
    let profile = FixtureProfile::default();
    let (generated, expectations) = generate_demo(&profile);
    let feed: Vec<String> = generated.iter().map(|g| g.line.clone()).collect();
    let path = write_feed(&feed);
    let cfg = demo_config(&profile);
    let result = run(&Source::File(path.clone()), &cfg).unwrap();

    // Acceptance counters.
    assert_eq!(result.stats.invalid_lines, expectations.invalid_lines);
    assert_eq!(result.stats.duplicate_events, expectations.duplicate_lines);
    assert_eq!(result.stats.late_events, expectations.late_events);
    assert_eq!(result.stats.clock_wraps, expectations.clock_wraps);
    assert_eq!(
        result.stats.processed_events,
        expectations.valid_unique_events
    );
    assert!(result.detection.counters.windows > 0);
    assert!(result.detection.counters.empty_windows >= 1);

    // All three rules must fire exactly as labelled sessions.
    let hits = &result.detection.per_rule_hits;
    assert!(hits[RULE_PORT_SCAN] >= 1, "port scan detected");
    assert!(hits[RULE_BRUTE_FORCE] >= 1, "brute force detected");
    assert!(hits[RULE_OUTBOUND_BURST] >= 1, "outbound burst detected");

    // Labelled evaluation: every labelled session detected, no misses, and the
    // benign background must not create false positives on tuned thresholds.
    assert_eq!(
        result.detection.evaluation.false_negatives,
        0,
        "all labelled anomalies must be detected"
    );
    // All labelled sessions are true positives (label propagated across the
    // whole attack campaign), and all three campaign labels are represented.
    assert_eq!(
        result.detection.evaluation.true_positives,
        result.detection.evaluation.truth_sessions
    );
    assert!(result.detection.evaluation.truth_sessions >= expectations.labelled_sessions);
    assert_eq!(result.detection.evaluation.false_positives, 0);
    assert_eq!(
        format!("{:.3}", result.detection.evaluation.rule_hit_rate()),
        "1.000"
    );

    let _ = std::fs::remove_file(path);
}

#[test]
fn replay_is_stable_and_idempotent() {
    let profile = FixtureProfile::default();
    let (generated, _) = generate_demo(&profile);
    let feed: Vec<String> = generated.iter().map(|g| g.line.clone()).collect();
    let path = write_feed(&feed);
    let cfg = demo_config(&profile);

    let first = run(&Source::File(path.clone()), &cfg).unwrap();
    let second = run(&Source::File(path.clone()), &cfg).unwrap();

    assert_eq!(first.stats.processed_events, second.stats.processed_events);
    assert_eq!(first.stats.rule_hits, second.stats.rule_hits);
    assert_eq!(
        first.detection.counters.anomalous_sessions,
        second.detection.counters.anomalous_sessions
    );
    assert_eq!(
        first.detection.evaluation.true_positives,
        second.detection.evaluation.true_positives
    );
    // Within a single run, duplicates are counted but never inserted twice.
    assert_eq!(first.stats.duplicate_events, 20);
    let _ = std::fs::remove_file(path);
}

#[test]
fn late_events_are_counted_but_never_correlated() {
    let profile = FixtureProfile::default();
    let (generated, _) = generate_demo(&profile);
    let feed: Vec<String> = generated.iter().map(|g| g.line.clone()).collect();
    let path = write_feed(&feed);
    let cfg = demo_config(&profile);
    let result = run(&Source::File(path.clone()), &cfg).unwrap();

    // Late events exist and are persisted in the store count.
    assert!(result.stats.late_events >= 6);
    // None of the late digests appear in any session evidence chain.
    let late_digests: std::collections::HashSet<String> = generated
        .iter()
        .filter(|g| g.late)
        .map(|g| edgewatch::model::fnv1a_64_hex(g.line.trim().as_bytes()))
        .collect();
    for s in &result.sessions {
        for d in &s.evidence {
            assert!(!late_digests.contains(d), "late digest leaked into session");
        }
    }
    let _ = std::fs::remove_file(path);
}

#[test]
fn ingest_batches_isolate_bad_lines_without_stopping() {
    let profile = FixtureProfile::default();
    let (generated, expectations) = generate_demo(&profile);
    let feed: Vec<u8> = generated
        .iter()
        .map(|g| g.line.clone())
        .collect::<Vec<_>>()
        .join("\n")
        .into_bytes();
    let mut stats = IngestStats::default();
    let batches = read_batches(Cursor::new(feed), 1000, &mut stats);
    assert!(batches.len() > 1);
    assert_eq!(stats.invalid_lines, expectations.invalid_lines);
    assert_eq!(
        stats.parsed_events,
        expectations.total_lines - expectations.invalid_lines
    );
    // Every reason category present in the crafted corrupt set is represented.
    for reason in [
        "empty_line",
        "unknown_format",
        "missing_field",
        "bad_address",
        "bad_port",
        "bad_timestamp",
        "bad_enum",
        "bad_json",
    ] {
        assert!(
            stats.by_reason.contains_key(reason),
            "missing invalid reason {reason}"
        );
    }
}

#[test]
fn all_bad_batch_does_not_stop_later_batches() {
    // First batch is entirely corrupt; the second batch must still parse and
    // the batch boundary/line counters must remain accurate.
    let feed = [
        "not a log",
        "   ",
        "{broken json",
        r#"{"device":"g","ts":"2026-11-14T00:00:00Z","src_addr":"10.0.0.1","dst_addr":"10.0.0.2","protocol":"tcp","action":"allow"}"#,
        r#"{"device":"g","ts":"2026-11-14T00:00:01Z","src_addr":"10.0.0.1","dst_addr":"10.0.0.2","protocol":"tcp","action":"allow"}"#,
    ]
    .join("\n");
    let mut stats = IngestStats::default();
    let batches = read_batches(Cursor::new(feed.into_bytes()), 3, &mut stats);
    assert_eq!(batches.len(), 2, "trailing partial batch retained");
    assert_eq!(stats.total_lines, 5);
    assert_eq!(stats.invalid_lines, 3);
    assert_eq!(stats.parsed_events, 2);
    assert_eq!(batches[0].events.len(), 0, "first batch all invalid");
    assert_eq!(batches[1].events.len(), 2, "second batch still processed");
    assert_eq!(batches[1].first_line_no, 4);
    assert_eq!(batches[1].last_line_no, 5);
}

#[test]
fn cross_midnight_fixture_spans_two_utc_dates() {
    let profile = FixtureProfile::default();
    let (generated, _) = generate_demo(&profile);
    let feed: Vec<String> = generated.iter().map(|g| g.line.clone()).collect();
    let path = write_feed(&feed);
    let cfg = demo_config(&profile);
    let result = run(&Source::File(path.clone()), &cfg).unwrap();

    let max_ts = result
        .on_time_events
        .iter()
        .map(|e| e.ts_us)
        .max()
        .unwrap();
    // Origin is 23:30 UTC; the 8-minute stream crosses 00:00 the next day.
    assert!(max_ts > ORIGIN_US);
    assert!(edgewatch::time::format_rfc3339_us(max_ts).starts_with("2026-11-15"));
    let _ = std::fs::remove_file(path);
}

#[test]
fn overlapping_windows_count_each_session_once() {
    // step < window: every session still lands in exactly one slot.
    let profile = FixtureProfile::default();
    let (generated, _) = generate_demo(&profile);
    let feed: Vec<String> = generated.iter().map(|g| g.line.clone()).collect();
    let path = write_feed(&feed);
    let mut cfg = demo_config(&profile);
    cfg.window_secs = 120;
    cfg.step_secs = 30;
    let result = run(&Source::File(path.clone()), &cfg).unwrap();

    let summed: u64 = result.detection.windows.iter().map(|w| w.sessions.len() as u64).sum();
    assert_eq!(summed, result.sessions.len() as u64);
    let _ = std::fs::remove_file(path);
}

#[test]
fn detection_config_defaults_are_sane() {
    let d = DetectionConfig::default();
    assert_eq!(d.window_secs, 60);
    assert_eq!(d.step_secs, 60);
    assert_eq!(d.score_threshold, 1);
}

#[test]
fn benchmark_feed_is_deterministic_and_parseable() {
    let a = generate_benchmark(5_000);
    let b = generate_benchmark(5_000);
    assert_eq!(a, b);
    let feed = a.join("\n").into_bytes();
    let mut stats = IngestStats::default();
    let batches = read_batches(Cursor::new(feed), 1000, &mut stats);
    assert_eq!(stats.invalid_lines, 0);
    assert!(batches.iter().map(|x| x.events.len()).sum::<usize>() == 5000);
}
