//! Timestamp parsing, UTC normalization, per-device watermarks with clock
//! wrap handling, and bounded out-of-order reorder buffers.
//!
//! # Timezone input rules
//! - RFC3339 style timestamps (`...Z` or `...+08:00`) must carry an explicit
//!   zone; a *naive* timestamp without zone is rejected as `bad_timestamp`.
//! - Legacy syslog timestamps (`Nov 14 10:05:30`) carry no zone and no year;
//!   they use the configured `default_offset_secs` (default 0 = UTC) and
//!   `legacy_year` (default 2026). This rule is documented in the README.
//! - Internally every timestamp is UTC microseconds since the Unix epoch.

use crate::model::Event;

/// Parse an RFC3339 timestamp with mandatory explicit timezone into UTC
/// microseconds. Both `T` and space separators are accepted; fractional
/// seconds may have 1–6 digits.
pub fn parse_rfc3339_us(input: &str) -> Result<i64, String> {
    let s = input.trim();
    if s.is_empty() {
        return Err("empty timestamp".to_string());
    }
    let (date_part, rest) = s
        .split_once(['T', ' '])
        .ok_or_else(|| format!("timestamp missing date/time separator: {s}"))?;

    // Date: YYYY-MM-DD
    let dparts: Vec<&str> = date_part.split('-').collect();
    if dparts.len() != 3 {
        return Err(format!("bad date in timestamp: {s}"));
    }
    let year: i32 = dparts[0]
        .parse()
        .map_err(|_| format!("bad year in timestamp: {s}"))?;
    let month: u32 = dparts[1]
        .parse()
        .map_err(|_| format!("bad month in timestamp: {s}"))?;
    let day: u32 = dparts[2]
        .parse()
        .map_err(|_| format!("bad day in timestamp: {s}"))?;
    if !(1..=12).contains(&month) || !(1..=31).contains(&day) {
        return Err(format!("out-of-range date in timestamp: {s}"));
    }

    // Split time body from zone suffix.
    let bytes = rest.as_bytes();
    let zone_pos = bytes
        .iter()
        .position(|&c| c == b'Z' || c == b'z' || c == b'+' || c == b'-')
        .ok_or_else(|| format!("naive timestamp without timezone is rejected: {s}"))?;
    let (time_body, zone_body) = rest.split_at(zone_pos);
    let offset_secs = parse_zone(zone_body)?;

    // Time: HH:MM:SS[.fraction]
    let (hms, frac) = match time_body.split_once('.') {
        Some((h, f)) => (h, parse_frac(f)?),
        None => (time_body, 0),
    };
    let tparts: Vec<&str> = hms.split(':').collect();
    if tparts.len() != 3 {
        return Err(format!("bad time in timestamp: {s}"));
    }
    let hour: u32 = tparts[0]
        .parse()
        .map_err(|_| format!("bad hour in timestamp: {s}"))?;
    let minute: u32 = tparts[1]
        .parse()
        .map_err(|_| format!("bad minute in timestamp: {s}"))?;
    let second: u32 = tparts[2]
        .parse()
        .map_err(|_| format!("bad second in timestamp: {s}"))?;
    if hour > 23 || minute > 59 || second > 60 {
        return Err(format!("out-of-range time in timestamp: {s}"));
    }

    let day_seconds = (hour as i64) * 3600 + (minute as i64) * 60 + (second as i64);
    let local_epoch_s = days_from_civil(year, month, day) * 86400 + day_seconds;
    let utc_s = local_epoch_s - offset_secs as i64;
    utc_s
        .checked_mul(1_000_000)
        .and_then(|v| v.checked_add(frac))
        .ok_or_else(|| format!("timestamp out of representable range: {s}"))
}

/// Parse a legacy `MMM dd hh:mm:ss` syslog timestamp. Zone and year come from
/// explicit configuration rather than being guessed.
pub fn parse_legacy_syslog_us(
    input: &str,
    default_offset_secs: i32,
    legacy_year: i32,
) -> Result<i64, String> {
    let parts: Vec<&str> = input.split_whitespace().collect();
    if parts.len() != 3 {
        return Err(format!("bad legacy syslog timestamp: {input}"));
    }
    let month = match parts[0] {
        "Jan" => 1u32,
        "Feb" => 2,
        "Mar" => 3,
        "Apr" => 4,
        "May" => 5,
        "Jun" => 6,
        "Jul" => 7,
        "Aug" => 8,
        "Sep" => 9,
        "Oct" => 10,
        "Nov" => 11,
        "Dec" => 12,
        other => return Err(format!("unknown month {other} in timestamp: {input}")),
    };
    let day: u32 = parts[1]
        .parse()
        .ok()
        .filter(|d| (1..=31).contains(d))
        .ok_or_else(|| format!("bad day in legacy timestamp: {input}"))?;
    let hms: Vec<&str> = parts[2].split(':').collect();
    if hms.len() != 3 {
        return Err(format!("bad time in legacy timestamp: {input}"));
    }
    let hour: u32 = hms[0]
        .parse()
        .map_err(|_| format!("bad hour in legacy timestamp: {input}"))?;
    let minute: u32 = hms[1]
        .parse()
        .map_err(|_| format!("bad minute in legacy timestamp: {input}"))?;
    let second: u32 = hms[2]
        .parse()
        .map_err(|_| format!("bad second in legacy timestamp: {input}"))?;
    if hour > 23 || minute > 59 || second > 59 {
        return Err(format!("out-of-range legacy timestamp: {input}"));
    }
    let local_epoch_s = days_from_civil(legacy_year, month, day) * 86400
        + (hour as i64) * 3600
        + (minute as i64) * 60
        + second as i64;
    Ok(local_epoch_s * 1_000_000 - (default_offset_secs as i64) * 1_000_000)
}

/// Format UTC microseconds as an RFC3339 `Z` timestamp with microsecond
/// precision — used for deterministic reports and fixture generation.
pub fn format_rfc3339_us(ts_us: i64) -> String {
    let mut secs = ts_us.div_euclid(1_000_000);
    let micros = ts_us.rem_euclid(1_000_000);
    let days = secs.div_euclid(86400);
    secs = secs.rem_euclid(86400);
    let (year, month, day) = civil_from_days(days);
    let hour = secs / 3600;
    let minute = (secs % 3600) / 60;
    let second = secs % 60;
    format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}.{:06}Z",
        year, month, day, hour, minute, second, micros
    )
}

fn parse_zone(zone: &str) -> Result<i32, String> {
    if zone.eq_ignore_ascii_case("Z") {
        return Ok(0);
    }
    if zone.len() != 6 || !zone.is_char_boundary(3) {
        return Err(format!("bad timezone offset: {zone}"));
    }
    let sign = match &zone[..1] {
        "+" => 1i32,
        "-" => -1,
        _ => return Err(format!("bad timezone offset: {zone}")),
    };
    if &zone[3..4] != ":" {
        return Err(format!("bad timezone offset: {zone}"));
    }
    let hh: i32 = zone[1..3]
        .parse()
        .map_err(|_| format!("bad timezone offset: {zone}"))?;
    let mm: i32 = zone[4..6]
        .parse()
        .map_err(|_| format!("bad timezone offset: {zone}"))?;
    if hh > 23 || mm > 59 {
        return Err(format!("timezone offset out of range: {zone}"));
    }
    Ok(sign * (hh * 3600 + mm * 60))
}

fn parse_frac(frac: &str) -> Result<i64, String> {
    if frac.is_empty() || frac.len() > 6 || !frac.bytes().all(|b| b.is_ascii_digit()) {
        return Err(format!("bad fractional seconds: {frac}"));
    }
    let v: i64 = frac
        .parse()
        .map_err(|_| format!("bad fractional seconds: {frac}"))?;
    Ok(v * 10i64.pow((6 - frac.len()) as u32))
}

/// Howard Hinnant's days-from-civil algorithm. Returns days since 1970-01-01
/// (may be negative for pre-epoch dates).
fn days_from_civil(y: i32, m: u32, d: u32) -> i64 {
    let y = y as i64 - if m <= 2 { 1 } else { 0 };
    let era = if y >= 0 { y } else { y - 399 } / 400;
    let yoe = y - era * 400;
    let doy = (153 * (if m > 2 { m - 3 } else { m + 9 }) as i64 + 2) / 5 + d as i64 - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146097 + doe - 719468
}

fn civil_from_days(z: i64) -> (i32, u32, u32) {
    let z = z + 719468;
    let era = if z >= 0 { z } else { z - 146096 } / 146097;
    let doe = z - era * 146097;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = if m <= 2 { y + 1 } else { y };
    (year as i32, m as u32, d as u32)
}

/// Per-device monotonic watermark with explicit clock-wrap correction.
///
/// A backward jump larger than `wrap_threshold_us` is treated as a device
/// clock wrap/reset: an accumulated shift is extended so post-wrap timestamps
/// remain ordered. Smaller backward jumps are ordinary out-of-order events
/// and are judged against `lateness_us` by [`ReorderBuffer`].
#[derive(Debug, Clone)]
pub struct Watermark {
    max_seen: i64,
    shift: i64,
    wrap_threshold_us: i64,
    wraps: u64,
}

impl Watermark {
    pub fn new(wrap_threshold_secs: i64) -> Self {
        Self {
            max_seen: i64::MIN,
            shift: 0,
            wrap_threshold_us: wrap_threshold_secs * 1_000_000,
            wraps: 0,
        }
    }

    pub fn wraps(&self) -> u64 {
        self.wraps
    }

    /// Normalize a raw device timestamp into the monotonic UTC timeline.
    pub fn normalize(&mut self, raw_ts: i64) -> i64 {
        if self.max_seen == i64::MIN {
            self.max_seen = raw_ts;
            return raw_ts;
        }
        let adjusted = raw_ts + self.shift;
        if self.max_seen - adjusted >= self.wrap_threshold_us {
            // Clock wrapped/reset: jump the shift so this event lands strictly
            // after the current watermark.
            self.shift += self.max_seen - adjusted + 1;
            self.wraps += 1;
            return raw_ts + self.shift;
        }
        if adjusted > self.max_seen {
            self.max_seen = adjusted;
        }
        adjusted
    }

    pub fn max_seen(&self) -> i64 {
        self.max_seen
    }
}

/// Result of pushing one event into a [`ReorderBuffer`].
#[derive(Debug)]
pub enum PushOutcome {
    /// Event accepted; these previously-buffered events are now older than the
    /// watermark tolerance and must be released in sorted order.
    Released(Vec<Event>),
    /// Event is within the reorder tolerance and held for later release.
    Buffered,
    /// Event arrives later than the tolerance allows. It is NOT dropped:
    /// callers persist it and count it separately, but it must not touch
    /// already-sealed windows.
    Late,
}

/// Bounded per-device out-of-order buffer. Memory grows with the lateness
/// horizon and event rate, never with total history.
#[derive(Debug, Clone)]
pub struct ReorderBuffer {
    watermark: Watermark,
    buffered: Vec<Event>,
    lateness_us: i64,
}

impl ReorderBuffer {
    pub fn new(lateness_secs: i64, wrap_threshold_secs: i64) -> Self {
        Self {
            watermark: Watermark::new(wrap_threshold_secs),
            buffered: Vec::new(),
            lateness_us: lateness_secs * 1_000_000,
        }
    }

    pub fn wraps(&self) -> u64 {
        self.watermark.wraps()
    }

    pub fn watermark_us(&self) -> i64 {
        self.watermark.max_seen()
    }

    pub fn buffered_len(&self) -> usize {
        self.buffered.len()
    }

    /// Feed one raw event. Its `ts_us` is rewritten to the wrap-corrected
    /// monotonic timestamp before buffering/release.
    pub fn push(&mut self, mut ev: Event) -> PushOutcome {
        let adjusted = self.watermark.normalize(ev.ts_us);
        ev.ts_us = adjusted;

        // Late: older than (max_seen - lateness). Compare against the
        // pre-update watermark position.
        let cutoff = self.watermark.max_seen() - self.lateness_us;
        if adjusted < cutoff {
            return PushOutcome::Late;
        }

        self.buffered.push(ev);
        // Release everything at or below the current tolerance cutoff.
        let mut released = Vec::new();
        self.buffered.retain(|e| {
            if e.ts_us <= cutoff {
                released.push(e.clone());
                false
            } else {
                true
            }
        });
        if released.is_empty() {
            PushOutcome::Buffered
        } else {
            released.sort_by(|a, b| {
                a.ts_us
                    .cmp(&b.ts_us)
                    .then_with(|| a.raw_digest.cmp(&b.raw_digest))
            });
            PushOutcome::Released(released)
        }
    }

    /// Drain all remaining events at end of stream, sorted deterministically.
    pub fn flush(mut self) -> Vec<Event> {
        self.buffered.sort_by(|a, b| {
            a.ts_us
                .cmp(&b.ts_us)
                .then_with(|| a.raw_digest.cmp(&b.raw_digest))
        });
        self.buffered
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rfc3339_utc_and_offset() {
        // 2026-11-14T00:00:00Z in epoch microseconds (computed constant).
        let midnight = days_from_civil(2026, 11, 14) * 86400 * 1_000_000;
        assert_eq!(
            parse_rfc3339_us("2026-11-14T00:00:00Z").unwrap(),
            midnight
        );
        // 08:00 at +08:00 is the same instant as 00:00 UTC.
        let utc = parse_rfc3339_us("2026-11-14T08:00:00+08:00").unwrap();
        assert_eq!(utc, midnight);
        let us = parse_rfc3339_us("2026-11-14T00:00:00.5Z").unwrap();
        assert_eq!(us - midnight, 500_000);
    }

    #[test]
    fn naive_timestamp_rejected() {
        assert!(parse_rfc3339_us("2026-11-14T00:00:00").is_err());
    }

    #[test]
    fn cross_midnight_ordering() {
        let a = parse_rfc3339_us("2026-11-14T23:59:59Z").unwrap();
        let b = parse_rfc3339_us("2026-11-15T00:00:00Z").unwrap();
        assert!(b > a);
        assert_eq!(b - a, 1_000_000);
    }

    #[test]
    fn reorder_buffer_releases_in_order_and_flags_late() {
        let mut buf = ReorderBuffer::new(2, 3600);
        let base = parse_rfc3339_us("2026-11-14T00:00:00Z").unwrap();
        let mk = |ts: i64, tag: &str| Event {
            device: "d1".into(),
            ts_us: ts,
            src_addr: "10.0.0.1".into(),
            src_port: Some(1),
            dst_addr: "10.0.0.2".into(),
            dst_port: Some(2),
            protocol: crate::model::Protocol::Tcp,
            action: crate::model::Action::Data,
            rule_label: None,
            account: None,
            bytes: None,
            event_id: Some(tag.into()),
            raw_digest: tag.into(),
            content_hash: tag.into(),
            source_format: crate::model::InputFormat::JsonLine,
            truth: None,
        };
        buf.push(mk(base, "a"));
        buf.push(mk(base + 500_000, "b"));
        let out = buf.push(mk(base + 3_000_000, "c")); // advances wm past tolerance
        match out {
            PushOutcome::Released(v) => {
                assert_eq!(v.iter().map(|e| e.event_id.as_deref().unwrap()).collect::<Vec<_>>(),
                    vec!["a", "b"]);
            }
            other => panic!("expected release, got {other:?}"),
        }
        // 10s behind a 3s watermark with 2s tolerance -> late
        let late = buf.push(mk(base - 7_000_000, "late"));
        assert!(matches!(late, PushOutcome::Late));
        let rest = buf.flush();
        assert_eq!(rest.len(), 1);
    }

    #[test]
    fn clock_wrap_is_corrected() {
        let mut wm = Watermark::new(3600);
        let base = 1_794_585_600_000_000i64;
        let a = wm.normalize(base);
        let b = wm.normalize(base + 1_000_000);
        // device resets its clock two hours backwards
        let c = wm.normalize(base - 7_200_000_000);
        assert!(c > b);
        assert_eq!(wm.wraps(), 1);
        assert_eq!(a, base);
    }
}
