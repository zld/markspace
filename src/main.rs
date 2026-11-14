//! edgewatch command-line entry point: argument parsing, run orchestration
//! and process exit codes.
//!
//! Exit codes:
//! - 0 success
//! - 1 runtime/pipeline error (details include batch and line context)
//! - 2 invalid CLI arguments

use clap::Parser;
use edgewatch::correlate::CorrelatorConfig;
use edgewatch::error::AppError;
use edgewatch::fixtures::{self, FixtureProfile};
use edgewatch::ingest::Source;
use edgewatch::{report, AppConfig};
use std::io::Write;
use std::process::ExitCode;

/// Offline edge-gateway log analyzer: normalize, correlate sessions and
/// detect anomalies entirely on the local machine.
#[derive(Parser, Debug)]
#[command(name = "edgewatch", version, about, long_about = None)]
struct Cli {
    /// Run the built-in synthetic demo dataset (no input file needed).
    #[arg(long)]
    demo: bool,

    /// Input file. If omitted and --demo is not set, lines are read from
    /// standard input.
    #[arg(long, value_name = "PATH")]
    input: Option<std::path::PathBuf>,

    /// Sliding detection window length in seconds.
    #[arg(long, default_value_t = 60)]
    window_secs: i64,

    /// Sliding window step in seconds (<= window-secs).
    #[arg(long, default_value_t = 60)]
    step_secs: i64,

    /// Out-of-order tolerance in seconds; events older than this are counted
    /// as late and never modify sealed windows.
    #[arg(long, default_value_t = 15)]
    lateness_secs: i64,

    /// Anomaly score at or above which a window is flagged.
    #[arg(long, default_value_t = 1)]
    score_threshold: u64,

    /// Input lines per ingest/transaction batch.
    #[arg(long, default_value_t = 5000)]
    batch_size: u64,

    /// Backward clock jump (seconds) at or beyond which a device is treated as
    /// having wrapped/reset its clock.
    #[arg(long, default_value_t = 3600)]
    wrap_threshold_secs: i64,

    /// UTC offset in seconds assumed for legacy syslog timestamps.
    #[arg(long, default_value_t = 0)]
    legacy_offset_secs: i32,

    /// Year assumed for legacy syslog timestamps.
    #[arg(long, default_value_t = 2026)]
    legacy_year: i32,

    // ---- correlation rule tuning ----
    #[arg(long, default_value_t = 30)]
    scan_window_secs: i64,
    #[arg(long, default_value_t = 10)]
    scan_min_ports: usize,
    #[arg(long, default_value_t = 5)]
    brute_max_failures: usize,
    #[arg(long, default_value_t = 120)]
    brute_window_secs: i64,
    #[arg(long, default_value_t = 1_000_000)]
    burst_bytes: u64,
    #[arg(long, default_value_t = 60)]
    udp_idle_secs: i64,
    #[arg(long, default_value_t = 300)]
    tcp_idle_secs: i64,

    /// Optional file-backed SQLite database (defaults to in-memory).
    #[arg(long, value_name = "DB_PATH")]
    db: Option<std::path::PathBuf>,

    /// Throughput self-check: generate N synthetic lines and report rate.
    #[arg(long, value_name = "N")]
    benchmark: Option<u64>,
}

impl Cli {
    fn to_config(&self) -> AppConfig {
        AppConfig {
            window_secs: self.window_secs,
            step_secs: self.step_secs,
            lateness_secs: self.lateness_secs,
            score_threshold: self.score_threshold,
            batch_size: self.batch_size,
            wrap_threshold_secs: self.wrap_threshold_secs,
            legacy_offset_secs: self.legacy_offset_secs,
            legacy_year: self.legacy_year,
            db_path: self.db.clone(),
            correlate: CorrelatorConfig {
                scan_window_us: self.scan_window_secs * 1_000_000,
                scan_min_ports: self.scan_min_ports,
                brute_max_failures: self.brute_max_failures,
                brute_window_us: self.brute_window_secs * 1_000_000,
                burst_bytes: self.burst_bytes,
                udp_idle_us: self.udp_idle_secs * 1_000_000,
                tcp_idle_us: self.tcp_idle_secs * 1_000_000,
            },
        }
    }
}

fn main() -> ExitCode {
    let cli = Cli::parse();
    let cfg = cli.to_config();
    if let Err(e) = cfg.validate() {
        eprintln!("edgewatch: {e}");
        return ExitCode::from(2);
    }

    match run(&cli, cfg) {
        Ok(code) => code,
        Err(AppError::BadArg(msg)) => {
            eprintln!("edgewatch: {msg}");
            ExitCode::from(2)
        }
        Err(e) => {
            eprintln!("edgewatch: {e}");
            ExitCode::from(1)
        }
    }
}

fn run(cli: &Cli, cfg: AppConfig) -> Result<ExitCode, AppError> {
    if let Some(n) = cli.benchmark {
        return benchmark(n, &cfg);
    }

    if cli.demo {
        let profile = FixtureProfile {
            lateness_secs: cli.lateness_secs,
            scan_min_ports: cli.scan_min_ports,
            brute_failures: cli.brute_max_failures,
            burst_bytes: cli.burst_bytes,
        };
        let (lines, _expectations) = fixtures::generate_demo(&profile);
        let feed: Vec<String> = lines.iter().map(|g| g.line.clone()).collect();
        let tmp = write_temp_feed(&feed)?;
        let source = Source::File(tmp.path().to_path_buf());
        let result = edgewatch::run(&source, &cfg)?;
        print!("{}", report::render(&result));
        return Ok(ExitCode::SUCCESS);
    }

    let source = match &cli.input {
        Some(path) => Source::File(path.clone()),
        None => Source::Stdin,
    };
    let result = edgewatch::run(&source, &cfg)?;
    print!("{}", report::render(&result));
    Ok(ExitCode::SUCCESS)
}

fn benchmark(n: u64, cfg: &AppConfig) -> Result<ExitCode, AppError> {
    let lines = fixtures::generate_benchmark(n);
    let tmp = write_temp_feed(&lines)?;
    let source = Source::File(tmp.path().to_path_buf());
    let result = edgewatch::run(&source, cfg)?;
    let rate = if result.elapsed.as_secs_f64() > 0.0 {
        result.stats.total_lines as f64 / result.elapsed.as_secs_f64()
    } else {
        0.0
    };
    let mut stdout = std::io::stdout().lock();
    writeln!(stdout, "benchmark_lines={}", result.stats.total_lines)?;
    writeln!(stdout, "benchmark_elapsed_ms={}", result.elapsed.as_millis())?;
    writeln!(stdout, "benchmark_lines_per_sec={rate:.0}")?;
    Ok(ExitCode::SUCCESS)
}

/// Persist a generated feed to an OS temp directory for the file-based source.
fn write_temp_feed(lines: &[String]) -> Result<temp_file::TempFile, AppError> {
    let mut tmp = temp_file::TempFile::new()?;
    for l in lines {
        writeln!(tmp.file(), "{l}")?;
    }
    tmp.file().flush()?;
    Ok(tmp)
}

/// Minimal temp-file helper so no extra crate is required.
mod temp_file {
    use std::fs::{File, OpenOptions};
    use std::path::PathBuf;

    pub struct TempFile {
        path: PathBuf,
        file: File,
    }

    impl TempFile {
        pub fn new() -> Result<Self, std::io::Error> {
            let base = std::env::temp_dir();
            let unique = format!(
                "edgewatch-{}-{}.log",
                std::process::id(),
                std::time::SystemTime::now()
                    .duration_since(std::time::UNIX_EPOCH)
                    .map(|d| d.as_nanos())
                    .unwrap_or(0)
            );
            let path = base.join(unique);
            let file = OpenOptions::new()
                .create_new(true)
                .read(true)
                .write(true)
                .open(&path)?;
            Ok(Self { path, file })
        }

        pub fn file(&mut self) -> &File {
            &self.file
        }

        pub fn path(&self) -> &std::path::Path {
            &self.path
        }
    }

    impl Drop for TempFile {
        fn drop(&mut self) {
            let _ = std::fs::remove_file(&self.path);
        }
    }
}
