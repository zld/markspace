//! Hand-written parsers for the two supported line formats:
//!
//! 1. JSON lines — one JSON object per line, RFC3339 timestamp with an
//!    explicit timezone.
//! 2. Simplified syslog — `<pri>MMM dd hh:mm:ss host app[pid]: k=v …` with a
//!    key/value payload.
//!
//! Both parsers emit the same normalized [`Event`]. Unknown formats, missing
//! fields, illegal addresses/ports/enums and bad timestamps are returned as
//! [`ParseError`] with a reason code so the ingest layer can isolate them.

use crate::error::{excerpt, ParseError, ParseReason};
use crate::model::{
    content_hash_of, fnv1a_64_hex, Action, Event, InputFormat, Protocol,
};
use crate::time::{parse_legacy_syslog_us, parse_rfc3339_us};

/// Defaults for legacy syslog timestamps (documented timezone input rule).
pub const DEFAULT_OFFSET_SECS: i32 = 0;
pub const DEFAULT_LEGACY_YEAR: i32 = 2026;

struct Ctx {
    batch_id: u64,
    line_no: u64,
    raw: String,
}

impl Ctx {
    fn err(&self, reason: ParseReason, detail: impl Into<String>) -> ParseError {
        ParseError {
            batch_id: self.batch_id,
            line_no: self.line_no,
            reason,
            detail: detail.into(),
            excerpt: excerpt(&self.raw),
        }
    }
}

/// Parse one raw line into a normalized event.
pub fn parse_line(raw: &str, batch_id: u64, line_no: u64) -> Result<Event, ParseError> {
    parse_line_with_time_cfg(
        raw,
        batch_id,
        line_no,
        DEFAULT_OFFSET_SECS,
        DEFAULT_LEGACY_YEAR,
    )
}

/// Parse with explicit legacy-syslog timezone/year configuration.
pub fn parse_line_with_time_cfg(
    raw: &str,
    batch_id: u64,
    line_no: u64,
    default_offset_secs: i32,
    legacy_year: i32,
) -> Result<Event, ParseError> {
    let trimmed = raw.trim();
    let ctx = Ctx {
        batch_id,
        line_no,
        raw: raw.to_string(),
    };
    if trimmed.is_empty() {
        return Err(ctx.err(ParseReason::EmptyLine, "line is empty"));
    }
    if trimmed.starts_with('{') {
        parse_json(trimmed, &ctx)
    } else if looks_like_syslog(trimmed) {
        parse_syslog(trimmed, &ctx, default_offset_secs, legacy_year)
    } else {
        Err(ctx.err(
            ParseReason::UnknownFormat,
            "line is neither a JSON object nor a syslog record",
        ))
    }
}

fn looks_like_syslog(s: &str) -> bool {
    // Optional <pri>, then a three-letter month token opening the line.
    let body = match s.strip_prefix('<') {
        Some(rest) => rest.contains('>') && rest.split_once('>').is_some_and(|(_, t)| t.starts_with_month()),
        None => s.starts_with_month(),
    };
    body && s.contains(": ")
}

trait SyslogProbe {
    fn starts_with_month(&self) -> bool;
}

impl SyslogProbe for str {
    fn starts_with_month(&self) -> bool {
        const MONTHS: [&str; 12] = [
            "Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec",
        ];
        MONTHS.iter().any(|m| self.starts_with(m))
    }
}

// ---------------------------------------------------------------------------
// JSON line parser
// ---------------------------------------------------------------------------

fn parse_json(s: &str, ctx: &Ctx) -> Result<Event, ParseError> {
    let v: serde_json::Value = serde_json::from_str(s)
        .map_err(|e| ctx.err(ParseReason::BadJson, format!("invalid JSON: {e}")))?;
    let obj = v
        .as_object()
        .ok_or_else(|| ctx.err(ParseReason::BadJson, "JSON line is not an object"))?;

    let get = |key: &str| -> Result<&serde_json::Value, ParseError> {
        obj.get(key)
            .filter(|v| !v.is_null())
            .ok_or_else(|| ctx.err(ParseReason::MissingField, format!("required field `{key}` absent")))
    };
    let str_field = |key: &str| -> Result<String, ParseError> {
        get(key)?
            .as_str()
            .map(|s| s.to_string())
            .ok_or_else(|| ctx.err(ParseReason::MissingField, format!("field `{key}` must be a string")))
    };
    let opt_str = |key: &str| -> Option<String> {
        obj.get(key).and_then(|v| v.as_str()).map(|s| s.to_string())
    };

    let device = str_field("device")?;
    let ts_raw = str_field("ts")?;
    let ts_us = parse_rfc3339_us(&ts_raw)
        .map_err(|e| ctx.err(ParseReason::BadTimestamp, e))?;
    let src_addr = validate_addr(&str_field("src_addr")?, ctx)?;
    let dst_addr = validate_addr(&str_field("dst_addr")?, ctx)?;
    let src_port = parse_opt_port(obj.get("src_port"), ctx)?;
    let dst_port = parse_opt_port(obj.get("dst_port"), ctx)?;
    let protocol = Protocol::parse(&str_field("protocol")?)
        .ok_or_else(|| ctx.err(ParseReason::BadEnum, "unknown protocol"))?;
    let action = Action::parse(&str_field("action")?)
        .ok_or_else(|| ctx.err(ParseReason::BadEnum, "unknown action"))?;
    let rule_label = opt_str("rule_label");
    let account = opt_str("account");
    let bytes = parse_opt_u64(obj.get("bytes"), ctx, "bytes")?;
    let event_id = opt_str("event_id");
    let truth = opt_str("truth");

    let raw_digest = fnv1a_64_hex(ctx.raw.trim().as_bytes());
    let content_hash = content_hash_of(
        &device,
        ts_us,
        &src_addr,
        src_port,
        &dst_addr,
        dst_port,
        protocol,
        action,
        &rule_label,
        &account,
        bytes,
        &event_id,
    );

    Ok(Event {
        device,
        ts_us,
        src_addr,
        src_port,
        dst_addr,
        dst_port,
        protocol,
        action,
        rule_label,
        account,
        bytes,
        event_id,
        raw_digest,
        content_hash,
        source_format: InputFormat::JsonLine,
        truth,
    })
}

// ---------------------------------------------------------------------------
// Simplified syslog parser
// ---------------------------------------------------------------------------

fn parse_syslog(
    s: &str,
    ctx: &Ctx,
    default_offset_secs: i32,
    legacy_year: i32,
) -> Result<Event, ParseError> {
    // Split header from key/value payload at the first ": ".
    let (header, payload) = s
        .split_once(": ")
        .ok_or_else(|| ctx.err(ParseReason::UnknownFormat, "syslog missing ': ' separator"))?;
    let header = header.strip_prefix('<').map_or(header, |rest| {
        rest.split_once('>').map(|(_pri, h)| h).unwrap_or(rest)
    });
    let htokens: Vec<&str> = header.split_whitespace().collect();
    if htokens.len() < 4 {
        return Err(ctx.err(
            ParseReason::UnknownFormat,
            "syslog header needs timestamp, host and app",
        ));
    }
    let ts_text = format!("{} {} {}", htokens[0], htokens[1], htokens[2]);
    let ts_us = parse_legacy_syslog_us(&ts_text, default_offset_secs, legacy_year)
        .map_err(|e| ctx.err(ParseReason::BadTimestamp, e))?;
    let device = htokens[3].to_string();

    let mut kv: std::collections::BTreeMap<String, String> = std::collections::BTreeMap::new();
    for token in payload.split_whitespace() {
        let Some((k, v)) = token.split_once('=') else {
            return Err(ctx.err(
                ParseReason::UnknownFormat,
                format!("syslog payload token `{token}` is not key=value"),
            ));
        };
        kv.insert(k.to_string(), v.to_string());
    }
    let need = |key: &str| -> Result<String, ParseError> {
        kv.get(key)
            .cloned()
            .ok_or_else(|| ctx.err(ParseReason::MissingField, format!("syslog missing `{key}`")))
    };
    let opt = |key: &str| kv.get(key).cloned();

    let src_addr = validate_addr(&need("src")?, ctx)?;
    let dst_addr = validate_addr(&need("dst")?, ctx)?;
    let src_port = parse_opt_port_str(opt("sport").as_deref(), ctx)?;
    let dst_port = parse_opt_port_str(opt("dport").as_deref(), ctx)?;
    let protocol = Protocol::parse(&need("proto")?)
        .ok_or_else(|| ctx.err(ParseReason::BadEnum, "unknown protocol"))?;
    let action = Action::parse(&need("act")?)
        .ok_or_else(|| ctx.err(ParseReason::BadEnum, "unknown action"))?;
    let rule_label = opt("rule");
    let account = opt("acct");
    let bytes = match opt("bytes") {
        Some(b) => Some(
            b.parse::<u64>()
                .map_err(|_| ctx.err(ParseReason::MissingField, "bytes is not an integer"))?,
        ),
        None => None,
    };
    let event_id = opt("eid");
    let truth = opt("truth");

    let raw_digest = fnv1a_64_hex(ctx.raw.trim().as_bytes());
    let content_hash = content_hash_of(
        &device,
        ts_us,
        &src_addr,
        src_port,
        &dst_addr,
        dst_port,
        protocol,
        action,
        &rule_label,
        &account,
        bytes,
        &event_id,
    );

    Ok(Event {
        device,
        ts_us,
        src_addr,
        dst_addr,
        src_port,
        dst_port,
        protocol,
        action,
        rule_label,
        account,
        bytes,
        event_id,
        raw_digest,
        content_hash,
        source_format: InputFormat::Syslog,
        truth,
    })
}

// ---------------------------------------------------------------------------
// Field validation
// ---------------------------------------------------------------------------

fn validate_addr(addr: &str, ctx: &Ctx) -> Result<String, ParseError> {
    let parts: Vec<&str> = addr.split('.').collect();
    if parts.len() != 4 {
        return Err(ctx.err(
            ParseReason::BadAddress,
            format!("`{addr}` is not a dotted-quad IPv4 address"),
        ));
    }
    for p in parts {
        match p.parse::<u16>() {
            Ok(n) if n <= 255 && !p.is_empty() => {}
            _ => {
                return Err(ctx.err(
                    ParseReason::BadAddress,
                    format!("`{addr}` has an octet out of 0..=255"),
                ))
            }
        }
    }
    Ok(addr.to_string())
}

fn parse_opt_port(v: Option<&serde_json::Value>, ctx: &Ctx) -> Result<Option<u16>, ParseError> {
    match v {
        None | Some(serde_json::Value::Null) => Ok(None),
        Some(serde_json::Value::Number(n)) => n
            .as_u64()
            .filter(|n| *n <= 65535)
            .map(|n| Some(n as u16))
            .ok_or_else(|| ctx.err(ParseReason::BadPort, "port out of 0..=65535")),
        Some(_) => Err(ctx.err(ParseReason::BadPort, "port must be an integer")),
    }
}

fn parse_opt_port_str(v: Option<&str>, ctx: &Ctx) -> Result<Option<u16>, ParseError> {
    match v {
        None => Ok(None),
        Some(s) => s
            .parse::<u16>()
            .map(Some)
            .map_err(|_| ctx.err(ParseReason::BadPort, format!("port `{s}` out of range"))),
    }
}

fn parse_opt_u64(
    v: Option<&serde_json::Value>,
    ctx: &Ctx,
    name: &str,
) -> Result<Option<u64>, ParseError> {
    match v {
        None | Some(serde_json::Value::Null) => Ok(None),
        Some(serde_json::Value::Number(n)) => n
            .as_u64()
            .map(Some)
            .ok_or_else(|| ctx.err(ParseReason::MissingField, format!("`{name}` must be unsigned"))),
        Some(_) => Err(ctx.err(ParseReason::MissingField, format!("`{name}` must be a number"))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn json_roundtrip_basic() {
        let line = r#"{"device":"gw-1","ts":"2026-11-14T10:00:00+08:00","src_addr":"10.0.0.1","src_port":41000,"dst_addr":"10.0.1.5","dst_port":443,"protocol":"tcp","action":"connect","event_id":"e1"}"#;
        let ev = parse_line(line, 1, 7).unwrap();
        assert_eq!(ev.device, "gw-1");
        assert_eq!(ev.dst_port, Some(443));
        assert_eq!(ev.source_format, InputFormat::JsonLine);
        // 10:00 +08:00 == 02:00 UTC
        assert_eq!(ev.ts_us, parse_rfc3339_us("2026-11-14T02:00:00Z").unwrap());
    }

    #[test]
    fn syslog_roundtrip_basic() {
        let line = "<134>Nov 14 10:00:00 gw-2 edgewatch[9]: proto=tcp src=10.0.0.2 sport=5000 dst=10.0.1.9 dport=22 act=connect eid=s1";
        let ev = parse_line(line, 2, 3).unwrap();
        assert_eq!(ev.device, "gw-2");
        assert_eq!(ev.protocol, Protocol::Tcp);
        assert_eq!(ev.event_id.as_deref(), Some("s1"));
        assert_eq!(ev.source_format, InputFormat::Syslog);
        assert!(ev.rule_label.is_none() && ev.bytes.is_none());
    }

    #[test]
    fn bad_lines_are_classified() {
        assert_eq!(
            parse_line("   ", 0, 0).unwrap_err().reason,
            ParseReason::EmptyLine
        );
        assert_eq!(
            parse_line("this is not a log line", 0, 0).unwrap_err().reason,
            ParseReason::UnknownFormat
        );
        let bad_addr = r#"{"device":"g","ts":"2026-11-14T00:00:00Z","src_addr":"999.1.1.1","dst_addr":"10.0.0.1","protocol":"tcp","action":"allow"}"#;
        assert_eq!(parse_line(bad_addr, 0, 0).unwrap_err().reason, ParseReason::BadAddress);
        let naive = r#"{"device":"g","ts":"2026-11-14T00:00:00","src_addr":"10.0.0.1","dst_addr":"10.0.0.2","protocol":"tcp","action":"allow"}"#;
        assert_eq!(parse_line(naive, 0, 0).unwrap_err().reason, ParseReason::BadTimestamp);
        let missing = r#"{"device":"g","ts":"2026-11-14T00:00:00Z","src_addr":"10.0.0.1","protocol":"tcp","action":"allow"}"#;
        assert_eq!(parse_line(missing, 0, 0).unwrap_err().reason, ParseReason::MissingField);
        let badport = r#"{"device":"g","ts":"2026-11-14T00:00:00Z","src_addr":"10.0.0.1","dst_addr":"10.0.0.2","src_port":70000,"protocol":"tcp","action":"allow"}"#;
        assert_eq!(parse_line(badport, 0, 0).unwrap_err().reason, ParseReason::BadPort);
    }

    #[test]
    fn credentials_are_redacted_from_excerpts() {
        let line = r#"GARBAGE password=hunter2 token=abcdef"#;
        let err = parse_line(line, 4, 11).unwrap_err();
        assert!(!err.excerpt.contains("hunter2"));
        assert!(!err.excerpt.contains("abcdef"));
        assert_eq!(err.batch_id, 4);
        assert_eq!(err.line_no, 11);
    }
}
