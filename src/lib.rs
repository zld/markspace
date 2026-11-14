//! edgewatch library crate: module layout and the end-to-end pipeline that
//! wires ingestion, time normalization, transactional storage, session
//! correlation and sliding-window detection together.

pub mod correlate;
pub mod detect;
pub mod error;
pub mod fixtures;
pub mod ingest;
pub mod model;
pub mod report;
pub mod store;
pub mod time;

use crate::correlate::{Correlator, CorrelatorConfig, Session};
use crate::detect::{DetectionConfig, DetectionOutcome};
use crate::ingest::{Batch, IngestStats, Source};
use crate::model::Event;
use crate::store::Store;
use crate::time::ReorderBuffer;
use std::collections::HashMap;

/// All tunables, populated from the CLI.
#[derive(Debug, Clone)]
pub struct AppConfig {
    pub window_secs: i64,
    pub step_secs: i64,
    pub lateness_secs: i64,
    pub score_threshold: u64,
    pub batch_size: u64,
    pub wrap_threshold_secs: i64,
    pub legacy_offset_secs: i32,
    pub legacy_year: i32,
    pub db_path: Option<std::path::PathBuf>,
    pub correlate: CorrelatorConfig,
}

impl AppConfig {
    pub fn validate(&self) -> error::AppResult<()> {
        if self.window_secs <= 0 || self.step_secs <= 0 || self.lateness_secs < 0 {
            return Err(error::AppError::BadArg(
                "window-secs and step-secs must be positive; lateness-secs must be >= 0".into(),
            ));
        }
        if self.step_secs > self.window_secs {
            return Err(error::AppError::BadArg(
                "step-secs must not exceed window-secs or windows would miss intervals".into(),
            ));
        }
        if self.batch_size == 0 {
            return Err(error::AppError::BadArg("batch-size must be positive".into()));
        }
        Ok(())
    }

    pub fn detection(&self) -> DetectionConfig {
        DetectionConfig {
            window_secs: self.window_secs,
            step_secs: self.step_secs,
            score_threshold: self.score_threshold,
        }
    }
}

/// Top-level counters surfaced in the terminal report. Every field is an
/// unsigned integer except the ratio fields.
#[derive(Debug, Clone, Default)]
pub struct RunStats {
    pub processed_events: u64,
    pub invalid_lines: u64,
    pub late_events: u64,
    pub duplicate_events: u64,
    pub inserted_events: u64,
    pub total_lines: u64,
    pub clock_wraps: u64,
    pub batches: u64,
    pub failed_batches: u64,
    pub rule_hits: u64,
}

/// Everything a run produces, ready for rendering.
pub struct PipelineResult {
    pub stats: RunStats,
    pub ingest: IngestStats,
    pub detection: DetectionOutcome,
    pub sessions: Vec<Session>,
    pub on_time_events: Vec<Event>,
    pub invalid_breakdown: Vec<(&'static str, u64)>,
    pub wraps_by_device: Vec<(String, u64)>,
    pub elapsed: std::time::Duration,
}

/// Run the full pipeline over a source.
///
/// Phases:
/// 1. Parse into batches, isolating bad lines without stopping later batches.
/// 2. Feed each event through a per-device reorder buffer (watermark + clock
///    wrap correction). Released and late events are persisted in per-batch
///    transactions; late events are recorded in a digest set so they are kept
///    but never touch correlation or sealed windows.
/// 3. Read every normalized event back from the store in timestamp order and
///    run a single, fully ordered correlation pass over non-late events.
/// 4. Build sliding windows using only the on-time stream, then evaluate rule
///    hits against ground-truth labels (labels never enter scoring).
pub fn run(source: &Source, cfg: &AppConfig) -> error::AppResult<PipelineResult> {
    cfg.validate()?;
    let started = std::time::Instant::now();
    let reader = ingest::open_source(source)?;
    let mut ingest_stats = IngestStats::default();
    let batches: Vec<Batch> = ingest::read_batches(reader, cfg.batch_size, &mut ingest_stats);

    let mut store = match &cfg.db_path {
        Some(p) => Store::open_file(p)?,
        None => Store::open_memory()?,
    };
    let mut buffers: HashMap<String, ReorderBuffer> = HashMap::new();
    let mut late_digests: std::collections::HashSet<String> = std::collections::HashSet::new();
    let mut stats = RunStats {
        total_lines: ingest_stats.total_lines,
        invalid_lines: ingest_stats.invalid_lines,
        ..RunStats::default()
    };

    // ---- Phases 1-2: reorder, clock-wrap correction, transactional writes.
    for batch in &batches {
        stats.batches += 1;
        let mut to_write: Vec<Event> = Vec::with_capacity(batch.events.len());
        for ev in &batch.events {
            let buf = buffers
                .entry(ev.device.clone())
                .or_insert_with(|| ReorderBuffer::new(cfg.lateness_secs, cfg.wrap_threshold_secs));
            match buf.push(ev.clone()) {
                time::PushOutcome::Released(released) => to_write.extend(released),
                time::PushOutcome::Buffered => {}
                time::PushOutcome::Late => {
                    // Persist but quarantine: never dropped, never correlated.
                    late_digests.insert(ev.raw_digest.clone());
                    to_write.push(ev.clone());
                    stats.late_events += 1;
                }
            }
        }
        let outcome =
            store.write_batch(batch.batch_id, batch.first_line_no, batch.last_line_no, &to_write)?;
        stats.duplicate_events += outcome.duplicates;
        stats.inserted_events += outcome.inserted;
    }

    // End-of-stream flush of every per-device buffer, in one final transaction.
    let mut wraps: HashMap<String, u64> = HashMap::new();
    let mut final_events: Vec<Event> = Vec::new();
    for (device, buf) in buffers.drain() {
        wraps.insert(device, buf.wraps());
        final_events.extend(buf.flush());
    }
    final_events.sort_by(|a, b| {
        a.ts_us
            .cmp(&b.ts_us)
            .then_with(|| a.raw_digest.cmp(&b.raw_digest))
    });
    if !final_events.is_empty() {
        let outcome = store.write_batch(stats.batches + 1, 0, 0, &final_events)?;
        stats.duplicate_events += outcome.duplicates;
        stats.inserted_events += outcome.inserted;
    }

    // ---- Phase 3: ordered read-back through the store, then correlate.
    let stored = store.all_events()?;
    let mut correlator = Correlator::new(cfg.correlate.clone());
    let mut on_time_events: Vec<Event> = Vec::with_capacity(stored.len());
    for ev in &stored {
        if late_digests.contains(&ev.raw_digest) {
            continue;
        }
        correlator.process(ev);
        on_time_events.push(ev.clone());
    }
    let sessions = correlator.finish();

    // ---- Phase 4: windows over the on-time stream + labelled evaluation.
    let detection = DetectionOutcome::build(&sessions, &on_time_events, &cfg.detection());
    stats.processed_events = store.count_events()?;
    stats.clock_wraps = wraps.values().sum();
    stats.rule_hits = detection.counters.rule_hits;

    let invalid_breakdown: Vec<(&'static str, u64)> =
        ingest_stats.by_reason.iter().map(|(k, v)| (*k, *v)).collect();
    let mut wraps_by_device: Vec<(String, u64)> = wraps.into_iter().collect();
    wraps_by_device.sort();

    Ok(PipelineResult {
        stats,
        ingest: ingest_stats,
        detection,
        sessions,
        on_time_events,
        invalid_breakdown,
        wraps_by_device,
        elapsed: started.elapsed(),
    })
}
