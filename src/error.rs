//! Error types used across edgewatch.
//!
//! Every parse failure carries the batch id and 1-based line number of the
//! offending input, plus a short redacted excerpt. Raw lines — which may
//! contain credentials — are never embedded in errors or reports.

use thiserror::Error;

pub type AppResult<T> = Result<T, AppError>;

#[derive(Debug, Error)]
pub enum AppError {
    #[error("io error: {0}")]
    Io(#[from] std::io::Error),

    #[error("sqlite error: {0}")]
    Sqlite(#[from] rusqlite::Error),

    #[error("invalid argument: {0}")]
    BadArg(String),

    /// A whole ingest batch failed to persist; the transaction has been
    /// rolled back. `location` identifies batch and line boundaries.
    #[error("batch write failed at {location}: {source}")]
    BatchFailed {
        location: String,
        #[source]
        source: rusqlite::Error,
    },

    #[error("{0}")]
    Other(String),
}

/// Why an individual line could not be parsed.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ParseReason {
    EmptyLine,
    UnknownFormat,
    MissingField,
    BadAddress,
    BadPort,
    BadTimestamp,
    BadEnum,
    BadJson,
}

impl ParseReason {
    pub fn as_str(self) -> &'static str {
        match self {
            ParseReason::EmptyLine => "empty_line",
            ParseReason::UnknownFormat => "unknown_format",
            ParseReason::MissingField => "missing_field",
            ParseReason::BadAddress => "bad_address",
            ParseReason::BadPort => "bad_port",
            ParseReason::BadTimestamp => "bad_timestamp",
            ParseReason::BadEnum => "bad_enum",
            ParseReason::BadJson => "bad_json",
        }
    }
}

/// One isolated bad line. The excerpt is capped and redacted; the full raw
/// line is retained nowhere.
#[derive(Debug, Clone)]
pub struct ParseError {
    pub batch_id: u64,
    pub line_no: u64,
    pub reason: ParseReason,
    pub detail: String,
    pub excerpt: String,
}

/// Replace obvious credential material in an excerpt before retaining it.
pub fn redact(raw: &str) -> String {
    let mut out = String::with_capacity(raw.len().min(160));
    for token in raw.split(|c: char| c.is_whitespace()) {
        let lower = token.to_ascii_lowercase();
        let sensitive = lower.contains("password")
            || lower.contains("passwd")
            || lower.contains("secret")
            || lower.contains("token");
        if !out.is_empty() {
            out.push(' ');
        }
        if sensitive {
            // Keep the key name if present, drop the value portion.
            if let Some(eq) = token.find('=') {
                out.push_str(&token[..=eq]);
                out.push_str("***");
            } else {
                out.push_str("***");
            }
        } else if token.len() > 48 {
            out.push_str(&token[..48]);
            out.push('…');
        } else {
            out.push_str(token);
        }
    }
    if out.len() > 120 {
        out.truncate(120);
        out.push('…');
    }
    out
}

/// Short single-line excerpt for error contexts (also redacted).
pub fn excerpt(raw: &str) -> String {
    redact(raw)
}
