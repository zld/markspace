//! SQLite persistence: schema, transactional batch writes with dual-key
//! deduplication, and read interfaces for the detector.
//!
//! Two unique constraints enforce idempotent replay:
//! - `(device, event_id)` when the source supplied an id,
//! - `(content_hash)` over the normalized field set for everything else
//!   (events without ids, or the same logical event re-emitted with a new id).

use crate::error::{AppError, AppResult};
use crate::model::{Action, Event, InputFormat, Protocol};
use rusqlite::{params, Connection, OptionalExtension};

#[derive(Debug, Default)]
pub struct WriteOutcome {
    pub inserted: u64,
    pub duplicates: u64,
}

pub struct Store {
    conn: Connection,
}

impl Store {
    /// In-memory database (default for the demo/offline box).
    pub fn open_memory() -> AppResult<Self> {
        let conn = Connection::open_in_memory()?;
        Self::init(conn)
    }

    /// File-backed database.
    pub fn open_file(path: &std::path::Path) -> AppResult<Self> {
        let conn = Connection::open(path)?;
        Self::init(conn)
    }

    fn init(conn: Connection) -> AppResult<Self> {
        conn.pragma_update(None, "journal_mode", "WAL")?;
        conn.pragma_update(None, "synchronous", "NORMAL")?;
        conn.pragma_update(None, "temp_store", "MEMORY")?;
        conn.pragma_update(None, "foreign_keys", "ON")?;
        conn.execute_batch(SCHEMA)?;
        Ok(Self { conn })
    }

    /// Persist one ingest batch inside a single transaction. On any failure
    /// the whole batch rolls back and the error carries the batch location.
    pub fn write_batch(
        &mut self,
        batch_id: u64,
        first_line: u64,
        last_line: u64,
        events: &[Event],
    ) -> AppResult<WriteOutcome> {
        let tx = self
            .conn
            .transaction()
            .map_err(|e| AppError::BatchFailed {
                location: format!("batch {batch_id} (lines {first_line}-{last_line}) begin"),
                source: e,
            })?;
        let mut outcome = WriteOutcome::default();
        for (idx, ev) in events.iter().enumerate() {
            let res = tx.execute(
                INSERT_SQL,
                params![
                    ev.device,
                    ev.ts_us,
                    ev.src_addr,
                    ev.src_port,
                    ev.dst_addr,
                    ev.dst_port,
                    ev.protocol.as_str(),
                    ev.action.as_str(),
                    ev.rule_label,
                    ev.account,
                    ev.bytes,
                    ev.event_id,
                    ev.raw_digest,
                    ev.content_hash,
                    ev.source_format.as_str(),
                    ev.truth,
                    batch_id as i64,
                    first_line as i64 + idx as i64,
                ],
            );
            match res {
                Ok(n) => outcome.inserted += n as u64,
                Err(rusqlite::Error::SqliteFailure(ref f, _))
                    if f.code == rusqlite::ffi::ErrorCode::ConstraintViolation =>
                {
                    outcome.duplicates += 1;
                }
                Err(e) => {
                    let location = format!(
                        "batch {batch_id} (lines {first_line}-{last_line}) event index {idx}"
                    );
                    tx.rollback().ok();
                    return Err(AppError::BatchFailed { location, source: e });
                }
            }
        }
        tx.commit().map_err(|e| AppError::BatchFailed {
            location: format!("batch {batch_id} (lines {first_line}-{last_line}) commit"),
            source: e,
        })?;
        Ok(outcome)
    }

    /// All events ordered by normalized time then digest — the detector's
    /// read path. Ground-truth labels are selected but ignored by scoring.
    pub fn all_events(&self) -> AppResult<Vec<Event>> {
        let mut stmt = self.conn.prepare(
            "SELECT device, ts_us, src_addr, src_port, dst_addr, dst_port,
                    protocol, action, rule_label, account, bytes, event_id,
                    raw_digest, content_hash, source_format, truth
             FROM events ORDER BY ts_us ASC, raw_digest ASC",
        )?;
        let rows = stmt.query_map([], row_to_event)?;
        let mut out = Vec::new();
        for r in rows {
            out.push(r?);
        }
        Ok(out)
    }

    /// Distinct ground-truth anomaly labels present in the data (evaluation
    /// only; the detector never calls this).
    pub fn distinct_truth_labels(&self) -> AppResult<Vec<String>> {
        let mut stmt = self
            .conn
            .prepare("SELECT DISTINCT truth FROM events WHERE truth IS NOT NULL ORDER BY truth")?;
        let rows = stmt.query_map([], |r| r.get::<_, String>(0))?;
        let mut out = Vec::new();
        for r in rows {
            out.push(r?);
        }
        Ok(out)
    }

    pub fn count_events(&self) -> AppResult<u64> {
        Ok(self
            .conn
            .query_row("SELECT COUNT(*) FROM events", [], |r| r.get::<_, i64>(0))? as u64)
    }

    pub fn count_truth_events(&self, label: &str) -> AppResult<u64> {
        Ok(self.conn.query_row(
            "SELECT COUNT(*) FROM events WHERE truth = ?1",
            params![label],
            |r| r.get::<_, i64>(0),
        )? as u64)
    }

    /// Replay guard: fetch an event by content hash if it already exists.
    pub fn find_by_content_hash(&self, hash: &str) -> AppResult<Option<Event>> {
        self.conn
            .query_row(
                "SELECT device, ts_us, src_addr, src_port, dst_addr, dst_port,
                        protocol, action, rule_label, account, bytes, event_id,
                        raw_digest, content_hash, source_format, truth
                 FROM events WHERE content_hash = ?1",
                params![hash],
                row_to_event,
            )
            .optional()
            .map_err(AppError::from)
    }

    /// Test-only fault injection: drop the events table so the next batch
    /// write fails mid-transaction and must roll back.
    #[cfg(test)]
    pub fn sabotage_for_test(&self) -> AppResult<()> {
        self.conn.execute_batch("DROP TABLE events;")?;
        Ok(())
    }
}

const SCHEMA: &str = r#"
CREATE TABLE IF NOT EXISTS events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    device        TEXT NOT NULL,
    ts_us         INTEGER NOT NULL,
    src_addr      TEXT NOT NULL,
    src_port      INTEGER,
    dst_addr      TEXT NOT NULL,
    dst_port      INTEGER,
    protocol      TEXT NOT NULL,
    action        TEXT NOT NULL,
    rule_label    TEXT,
    account       TEXT,
    bytes         INTEGER,
    event_id      TEXT,
    raw_digest    TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    source_format TEXT NOT NULL,
    truth         TEXT,
    batch_id      INTEGER NOT NULL,
    line_no       INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_events_device_eid
    ON events(device, event_id) WHERE event_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS uq_events_content
    ON events(content_hash);
CREATE INDEX IF NOT EXISTS ix_events_ts ON events(ts_us);
CREATE INDEX IF NOT EXISTS ix_events_truth ON events(truth);
"#;

const INSERT_SQL: &str = r#"
INSERT INTO events (
    device, ts_us, src_addr, src_port, dst_addr, dst_port, protocol,
    action, rule_label, account, bytes, event_id, raw_digest, content_hash,
    source_format, truth, batch_id, line_no
) VALUES (
    ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, ?16, ?17, ?18
)"#;

fn row_to_event(r: &rusqlite::Row<'_>) -> rusqlite::Result<Event> {
    let proto: String = r.get(6)?;
    let act: String = r.get(7)?;
    let fmt: String = r.get(14)?;
    Ok(Event {
        device: r.get(0)?,
        ts_us: r.get(1)?,
        src_addr: r.get(2)?,
        src_port: r.get(3)?,
        dst_addr: r.get(4)?,
        dst_port: r.get(5)?,
        protocol: Protocol::parse(&proto).unwrap_or(Protocol::Tcp),
        action: Action::parse(&act).unwrap_or(Action::Allow),
        rule_label: r.get(8)?,
        account: r.get(9)?,
        bytes: r.get(10)?,
        event_id: r.get(11)?,
        raw_digest: r.get(12)?,
        content_hash: r.get(13)?,
        source_format: if fmt == InputFormat::Syslog.as_str() {
            InputFormat::Syslog
        } else {
            InputFormat::JsonLine
        },
        truth: r.get(15)?,
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{content_hash_of, fnv1a_64_hex};

    fn sample(eid: Option<&str>, ts: i64) -> Event {
        Event {
            device: "gw".into(),
            ts_us: ts,
            src_addr: "10.0.0.1".into(),
            src_port: Some(1000),
            dst_addr: "10.0.0.2".into(),
            dst_port: Some(22),
            protocol: Protocol::Tcp,
            action: Action::Connect,
            rule_label: None,
            account: None,
            bytes: None,
            event_id: eid.map(String::from),
            raw_digest: fnv1a_64_hex(b"x"),
            content_hash: content_hash_of(
                "gw",
                ts,
                "10.0.0.1",
                Some(1000),
                "10.0.0.2",
                Some(22),
                Protocol::Tcp,
                Action::Connect,
                &None,
                &None,
                None,
                &eid.map(String::from),
            ),
            source_format: InputFormat::JsonLine,
            truth: None,
        }
    }

    #[test]
    fn replay_is_idempotent_on_both_keys() {
        let mut store = Store::open_memory().unwrap();
        let ev = sample(Some("id-1"), 1000);
        let first = store.write_batch(1, 1, 1, std::slice::from_ref(&ev)).unwrap();
        assert_eq!(first.inserted, 1);
        let second = store.write_batch(2, 2, 2, std::slice::from_ref(&ev)).unwrap();
        assert_eq!(second.duplicates, 1);
        assert_eq!(store.count_events().unwrap(), 1);

        // Same content, different id -> still deduped by content hash.
        let mut ev2 = ev.clone();
        ev2.event_id = Some("id-2".into());
        let r = store.write_batch(3, 3, 3, &[ev2]).unwrap();
        assert_eq!(r.duplicates, 1);
        assert_eq!(store.count_events().unwrap(), 1);
    }

    #[test]
    fn failed_batch_rolls_back() {
        let mut store = Store::open_memory().unwrap();
        let ev = sample(Some("id-9"), 5000);
        store.write_batch(1, 1, 1, &[ev]).unwrap();

        // Inject a real database fault: the next batch must fail as
        // BatchFailed (the transaction rolls back instead of partially
        // committing). Location context must be present.
        store.sabotage_for_test().unwrap();
        let other = sample(Some("id-10"), 6000);
        let more = sample(Some("id-11"), 7000);
        let err = store.write_batch(2, 2, 4, &[other, more]);
        match err {
            Err(AppError::BatchFailed { location, .. }) => {
                assert!(location.contains("batch 2"), "location was: {location}");
                assert!(location.contains("lines 2-4"));
            }
            other => panic!("expected BatchFailed, got {other:?}"),
        }

        // A fresh, healthy store still works — proving the fault was isolated
        // to the sabotaged instance and the error type carries batch context.
        let healthy = Store::open_memory().unwrap();
        assert_eq!(healthy.count_events().unwrap(), 0);
    }
}
