//! Input ingestion: source readers, batch boundaries and bad-line isolation.

pub mod parser;

use crate::error::{excerpt, ParseError, ParseReason};
use crate::model::Event;
use std::io::{BufRead, BufReader, Read};

/// Where input lines come from.
pub enum Source {
    Stdin,
    File(std::path::PathBuf),
}

/// Aggregated counters plus every isolated bad line (capped, redacted).
#[derive(Debug, Default, Clone)]
pub struct IngestStats {
    pub total_lines: u64,
    pub parsed_events: u64,
    pub invalid_lines: u64,
    pub by_reason: std::collections::BTreeMap<&'static str, u64>,
    pub invalid_records: Vec<ParseError>,
}

impl IngestStats {
    fn record_error(&mut self, err: ParseError) {
        self.invalid_lines += 1;
        *self.by_reason.entry(err.reason.as_str()).or_insert(0) += 1;
        // Keep the first N records only so a corrupt feed cannot grow memory
        // without bound; counters remain exact.
        if self.invalid_records.len() < 256 {
            self.invalid_records.append(&mut vec![err]);
        }
    }
}

/// One decoded batch: the good events in input order, stats and boundaries.
pub struct Batch {
    pub batch_id: u64,
    pub first_line_no: u64,
    pub last_line_no: u64,
    pub events: Vec<Event>,
}

/// Read lines from any source, split them into batches of at most
/// `batch_size` *input lines*, parse each line and isolate failures without
/// interrupting subsequent batches.
pub fn read_batches<R: Read>(
    reader: R,
    batch_size: u64,
    stats: &mut IngestStats,
) -> Vec<Batch> {
    let mut batches = Vec::new();
    let buf = BufReader::new(reader);
    let mut batch_id = 0u64;
    let mut line_no = 0u64;
    let mut current = Batch {
        batch_id: 0,
        first_line_no: 1,
        last_line_no: 0,
        events: Vec::new(),
    };
    let mut lines_in_batch = 0u64;

    for line in buf.lines() {
        line_no += 1;
        stats.total_lines += 1;
        let raw = match line {
            Ok(l) => l,
            Err(io_err) => {
                stats.record_error(ParseError {
                    batch_id,
                    line_no,
                    reason: ParseReason::UnknownFormat,
                    detail: format!("io read failure: {io_err}"),
                    excerpt: String::from("<unreadable line>"),
                });
                continue;
            }
        };
        if lines_in_batch == 0 {
            batch_id += 1;
            current.batch_id = batch_id;
            current.first_line_no = line_no;
        }
        lines_in_batch += 1;
        current.last_line_no = line_no;

        match parser::parse_line(&raw, batch_id, line_no) {
            Ok(ev) => {
                stats.parsed_events += 1;
                current.events.push(ev);
            }
            Err(err) => stats.record_error(err),
        }

        if lines_in_batch >= batch_size {
            batches.push(std::mem::replace(
                &mut current,
                Batch {
                    batch_id: batch_id + 1,
                    first_line_no: line_no + 1,
                    last_line_no: line_no,
                    events: Vec::new(),
                },
            ));
            lines_in_batch = 0;
        }
    }

    // Flush the trailing partial batch (if any input lines remained). A batch
    // may contain only invalid lines, so gate on line count, not event count.
    if lines_in_batch > 0 {
        batches.push(current);
    }
    batches
}

/// Open a [`Source`] into a boxed buffered reader.
pub fn open_source(source: &Source) -> std::io::Result<Box<dyn Read>> {
    match source {
        Source::Stdin => Ok(Box::new(std::io::stdin().lock())),
        Source::File(path) => Ok(Box::new(std::io::BufReader::new(std::fs::File::open(
            path,
        )?))),
    }
}

/// Helper used by tests/fixtures: parse a single line or produce a concise
/// error triplet.
pub fn parse_or_err(raw: &str) -> Result<Event, (ParseReason, String)> {
    parser::parse_line(raw, 0, 0).map_err(|e| (e.reason, e.detail))
}

/// Re-export so callers can build a redacted excerpt consistently.
pub fn safe_excerpt(raw: &str) -> String {
    excerpt(raw)
}
