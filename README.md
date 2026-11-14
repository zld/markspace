# edgewatch

Offline analysis of edge-gateway connection, authentication and policy logs
for a disconnected operator workstation. It normalizes heterogeneous log
lines into one UTC-microsecond event model, tolerates out-of-order delivery
and device clock wraps, deduplicates into an embedded SQLite store,
correlates events into sessions, and scores sliding-window anomalies with a
labelled false-positive / false-negative evaluation.

No network access, external services or real target traffic are required.
Every sample and test dataset is generated deterministically in code.

## Build

```sh
cargo build --release
```

All dependencies are fetched with the single Cargo invocation above
(rusqlite uses a bundled SQLite, so no system SQLite is needed).

## Quick start (self-contained demo)

```sh
cargo run --release -- --demo --window-secs 60 --lateness-secs 15
```

The demo generates a reproducible feed containing benign traffic, three known
attack campaigns, out-of-order and late events, a device clock wrap, a
cross-midnight span, duplicate replays and corrupt lines, then prints a
quantitative terminal report. Repeated runs produce identical counts.

Throughput self-check (synthetic N-line feed):

```sh
cargo run --release -- --benchmark 200000
```

## Inputs

Read a file, or standard input when `--input` is omitted and `--demo` is not
set:

```sh
edgewatch --input gateway.log --window-secs 60 --step-secs 30 --lateness-secs 15
cat gateway.log | edgewatch --window-secs 60
```

### Supported line formats

1. **JSON line** — one JSON object per line, RFC3339 timestamp with an
   **explicit** timezone (`Z` or `±HH:MM`). Naive timestamps without a zone
   are rejected as `bad_timestamp`.

   ```json
   {"device":"gw-1","ts":"2026-11-14T10:00:00+08:00","src_addr":"10.0.0.1","src_port":41000,"dst_addr":"10.0.1.5","dst_port":443,"protocol":"tcp","action":"connect","event_id":"e1"}
   ```

2. **Simplified syslog** — optional `<pri>`, a legacy `MMM dd hh:mm:ss`
   header (no zone, no year), host, app and a `key=value` payload.

   ```
   <134>Nov 14 10:00:00 gw-2 app[9]: proto=tcp src=10.0.0.2 sport=5000 dst=10.0.1.9 dport=22 act=connect eid=s1
   ```

### Timezone input rules

- RFC3339 timestamps must carry an explicit zone; they are converted to UTC.
- Legacy syslog timestamps have no zone or year. They use the configured
  `--legacy-offset-secs` (default `0`, UTC) and `--legacy-year`
  (default `2026`). Document these rather than guessing.
- Internally every timestamp is UTC microseconds since the Unix epoch.

Optional fields (`src_port`/`dst_port` for ICMP, `rule_label`, `account`,
`bytes`, `event_id`) are represented as explicit null/`Option`, never faked
as zero values.

## Data flow

```
fixture or stdin/file
  -> read & parse (bad lines isolated with batch + line context)
  -> per-device watermark + bounded reorder buffer (clock wrap correction)
  -> SQLite transactional batch writes with dual-key dedup
  -> ordered read-back -> session correlation state machine
  -> sliding-window detection -> labelled evaluation -> terminal report
```

- **Bad lines** are counted by reason (`empty_line`, `unknown_format`,
  `missing_field`, `bad_address`, `bad_port`, `bad_timestamp`, `bad_enum`,
  `bad_json`) and never stop later batches. Only short, redacted excerpts are
  retained; credential values and full raw lines are never printed.
- **Late events** (older than `--lateness-secs`) are persisted and counted
  separately but never reach the correlator, so sealed windows are immutable.
- **Clock wraps** — backward jumps at or beyond `--wrap-threshold-secs`
  (default 3600s) — shift the per-device monotonic timeline rather than being
  dropped.
- **Dedup** uses a partial unique index on `(device, event_id)` when an id is
  present and a unique index on a normalized `content_hash`, so replaying a
  batch is idempotent even if source ids change. A failed write rolls back the
  whole batch and reports the batch id and line range.

## Sessions and rules

Sessions are keyed by a direction-independent 5-tuple, merging bidirectional
traffic. TCP follows connect/disconnect; UDP closes on idle timeout; a reused
5-tuple after close starts a fresh session. Incomplete sessions are finalized
as `incomplete_at_eof`.

Configurable rules, each recording the triggering event count:

| Rule | Fires when |
| --- | --- |
| `RULE_PORT_SCAN` | `--scan-min-ports` distinct destination ports from one source within `--scan-window-secs` |
| `RULE_BRUTE_FORCE` | `--brute-max-failures` consecutive auth failures for one account, then a success within `--brute-window-secs` |
| `RULE_OUTBOUND_BURST` | one session's initiator outbound bytes reach `--burst-bytes` |

Each session is assigned to exactly one sliding-window slot, so overlapping
windows never double-count; empty windows between observed bounds are still
emitted.

## Report fields

The header always contains the acceptance counters:
`processed_events`, `invalid_lines`, `late_events`, `duplicate_events`,
`windows`, `anomalous_sessions`, `rule_hits`, `rule_hit_rate` (three-decimal
ratio), `false_positives`, `false_negatives`. It is followed by per-rule hits,
the labelled confusion matrix, invalid-line reasons, clock wraps by device,
per-window verdicts and an evidence section containing only session metadata
and raw-line digests.

Ground-truth `truth` labels exist solely for offline evaluation and never
participate in scoring.

## Exit codes

- `0` success
- `1` runtime / pipeline error (message carries batch and line context)
- `2` invalid CLI arguments

## Project layout

```
src/main.rs              CLI, orchestration, exit codes
src/ingest/mod.rs        source reading, batch boundaries, bad-line isolation
src/ingest/parser.rs     hand-written JSON-line and syslog parsers
src/time.rs              timestamp parsing, UTC normalization, watermark, reorder buffer
src/store.rs             SQLite schema, transactional dedup writes, queries
src/correlate.rs         session state machine, correlation rules, evidence
src/detect.rs            sliding windows, scoring, FP/FN evaluation
src/report.rs            deterministic terminal report
src/fixtures.rs          deterministic synthetic data generation
tests/generate_fixtures.rs  end-to-end integration tests
```
