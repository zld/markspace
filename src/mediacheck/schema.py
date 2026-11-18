"""SQLite schema for scan snapshots and compare runs."""

SCHEMA_VERSION = 1

_SCHEMA = """
CREATE TABLE IF NOT EXISTS meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS scans (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    sha256 TEXT NOT NULL,
    tool_version TEXT NOT NULL,
    container TEXT NOT NULL,
    size INTEGER NOT NULL,
    declared_duration_ms REAL,
    truncated INTEGER NOT NULL,
    error TEXT,
    metadata_json TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (sha256, tool_version)
);
CREATE TABLE IF NOT EXISTS scan_tracks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    scan_id INTEGER NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
    stable_index INTEGER NOT NULL,
    kind TEXT NOT NULL,
    language TEXT,
    codec TEXT,
    duration_ms REAL,
    sample_rate INTEGER,
    channels INTEGER,
    bit_depth INTEGER,
    block_align INTEGER,
    extra_json TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_scan_tracks_scan ON scan_tracks(scan_id);
CREATE TABLE IF NOT EXISTS scan_paths (
    relative_path TEXT PRIMARY KEY,
    scan_id INTEGER NOT NULL REFERENCES scans(id) ON DELETE CASCADE,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS compare_runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    duration_tolerance_ms REAL NOT NULL,
    files_scanned INTEGER NOT NULL,
    tracks_expected INTEGER NOT NULL,
    tracks_actual INTEGER NOT NULL,
    mismatch_count INTEGER NOT NULL,
    error_count INTEGER NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS compare_files (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id INTEGER NOT NULL REFERENCES compare_runs(id) ON DELETE CASCADE,
    relative_path TEXT NOT NULL,
    expected_scan_id INTEGER REFERENCES scans(id),
    actual_scan_id INTEGER NOT NULL REFERENCES scans(id),
    mismatch_count INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_compare_files_run ON compare_files(run_id);
CREATE TABLE IF NOT EXISTS compare_mismatches (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    file_id INTEGER NOT NULL REFERENCES compare_files(id) ON DELETE CASCADE,
    code TEXT NOT NULL,
    detail TEXT NOT NULL,
    expected_json TEXT,
    actual_json TEXT
);
CREATE INDEX IF NOT EXISTS idx_mismatches_file ON compare_mismatches(file_id);
"""
