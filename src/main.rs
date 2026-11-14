//! postpipe CLI: parses arguments, assembles the run configuration and
//! prints the final summary. All output is UTF-8 JSON Lines on stdout;
//! errors go to stderr with a non-zero exit code.

use postpipe::chain::{self, StepStatus};
use postpipe::config::ChainConfig;
use postpipe::metrics::PassMetrics;
use postpipe::{hash, image_source, PipelineError, MAX_DIMENSION, MAX_FRAMES};
use serde_json::json;
use std::process::ExitCode;

struct Cli {
    seed: u64,
    frames: u32,
    width: u32,
    height: u32,
    config: String,
}

fn usage() -> &'static str {
    "usage: postpipe --seed <u64> --frames <1-1000> --width <1-4096> --height <1-4096> --config <path>"
}

fn parse_args() -> Result<Cli, String> {
    let mut seed = None;
    let mut frames = None;
    let mut width = None;
    let mut height = None;
    let mut config = None;

    let mut it = std::env::args().skip(1);
    while let Some(arg) = it.next() {
        let (key, inline) = match arg.split_once('=') {
            Some((k, v)) => (k.to_string(), Some(v.to_string())),
            None => (arg, None),
        };
        let mut value = |name: &str| -> Result<String, String> {
            match &inline {
                Some(v) => Ok(v.clone()),
                None => it
                    .next()
                    .ok_or_else(|| format!("missing value for {name}")),
            }
        };
        let parse_num = |name: &str, s: String| -> Result<u64, String> {
            s.parse::<u64>()
                .map_err(|_| format!("invalid value for {name}: `{s}`"))
        };
        let parse_u32 = |name: &str, s: String| -> Result<u32, String> {
            let v = parse_num(name, s)?;
            u32::try_from(v).map_err(|_| format!("value for {name} is too large: {v}"))
        };
        match key.as_str() {
            "--seed" => seed = Some(parse_num("--seed", value("--seed")?)?),
            "--frames" => frames = Some(parse_u32("--frames", value("--frames")?)?),
            "--width" => width = Some(parse_u32("--width", value("--width")?)?),
            "--height" => height = Some(parse_u32("--height", value("--height")?)?),
            "--config" => config = Some(value("--config")?),
            "--help" | "-h" => return Err(usage().to_string()),
            other => return Err(format!("unknown argument `{other}`\n{}", usage())),
        }
    }

    Ok(Cli {
        seed: seed.ok_or("missing --seed")?,
        frames: frames.ok_or("missing --frames")?,
        width: width.ok_or("missing --width")?,
        height: height.ok_or("missing --height")?,
        config: config.ok_or("missing --config")?,
    })
}

fn validate_cli(cli: &Cli) -> Result<(), PipelineError> {
    if cli.frames == 0 || cli.frames > MAX_FRAMES {
        return Err(PipelineError::Config(format!(
            "--frames must be within 1..={MAX_FRAMES}, got {}",
            cli.frames
        )));
    }
    if cli.width == 0 || cli.width > MAX_DIMENSION || cli.height == 0 || cli.height > MAX_DIMENSION
    {
        return Err(PipelineError::InvalidDimensions {
            width: cli.width,
            height: cli.height,
            reason: format!("each side must be within 1..={MAX_DIMENSION}"),
        });
    }
    Ok(())
}

fn main() -> ExitCode {
    let cli = match parse_args() {
        Ok(c) => c,
        Err(e) => {
            eprintln!("error: {e}");
            return ExitCode::from(2);
        }
    };
    match run(&cli) {
        Ok(code) => ExitCode::from(code),
        Err(e) => {
            eprintln!("error: {e}");
            ExitCode::from(1)
        }
    }
}

fn run(cli: &Cli) -> Result<u8, PipelineError> {
    // Everything up to the first printed line must be side-effect free:
    // a bad configuration never starts processing and never produces a
    // forged success summary.
    validate_cli(cli)?;
    let text = std::fs::read_to_string(&cli.config)?;
    let cfg = ChainConfig::from_str(&text)?;
    cfg.validate()?;
    let passes = cfg.build_passes()?;
    let policy = cfg.failure_policy();
    let interrupt_at = cfg.interrupt_before_pass;

    let mut prev_hash: Option<String> = None;
    let mut prev_dims: Option<(u32, u32)> = None;
    let mut prev_metrics: Vec<Option<PassMetrics>> = vec![None; passes.len()];
    let mut adjacent_pairs: u64 = 0;
    let mut adjacent_matches: u64 = 0;
    let mut frames_completed: u32 = 0;
    let mut final_hash = String::new();
    let mut status = "ok";
    let mut exit_code = 0u8;

    for f in 0..cli.frames {
        let source = image_source::generate_frame(cli.seed, f, cli.width, cli.height)?;
        let outcome = chain::run_chain(source, &passes, policy, |i| interrupt_at == Some(i));

        // Cross-frame baseline: reset when dimensions change.
        if let Some((pw, ph)) = prev_dims {
            if (pw, ph) != (cli.width, cli.height) {
                println!(
                    "{}",
                    json!({
                        "type": "baseline_reset",
                        "frame": f,
                        "reason": format!(
                            "dimensions changed from {pw}x{ph} to {}x{}; cross-frame baseline reset",
                            cli.width, cli.height
                        ),
                    })
                );
                prev_metrics.iter_mut().for_each(|m| *m = None);
            }
        }

        for step in &outcome.steps {
            let delta = match (step.metrics, prev_metrics[step.index]) {
                (Some(cur), Some(prev)) => Some(json!({
                    "total_abs_diff": cur.total_abs_diff as i64 - prev.total_abs_diff as i64,
                    "changed_pixels": cur.changed_pixels as i64 - prev.changed_pixels as i64,
                })),
                _ => None,
            };
            println!(
                "{}",
                json!({
                    "type": "pass",
                    "frame": f,
                    "pass_index": step.index,
                    "name": step.name,
                    "status": match step.status {
                        StepStatus::Committed => "committed",
                        StepStatus::FailedRolledBack => "failed_rolled_back",
                    },
                    "input_hash": step.input_hash,
                    "output_hash": step.output_hash,
                    "metrics": step.metrics,
                    "error": step.error,
                    "delta_vs_prev_frame": delta,
                })
            );
            if step.metrics.is_some() {
                prev_metrics[step.index] = step.metrics;
            }
        }

        if let Some(i) = outcome.interrupted_at {
            println!(
                "{}",
                json!({
                    "type": "interrupt",
                    "frame": f,
                    "before_pass_index": i,
                    "committed_steps": outcome.steps.iter()
                        .filter(|s| s.status == StepStatus::Committed).count(),
                })
            );
            status = "interrupted";
        }

        let frame_hash = hash::frame_hash(&outcome.final_frame);
        let committed = outcome
            .steps
            .iter()
            .filter(|s| s.status == StepStatus::Committed)
            .count();
        println!(
            "{}",
            json!({
                "type": "frame",
                "frame": f,
                "width": cli.width,
                "height": cli.height,
                "passes_committed": committed,
                "passes_failed": outcome.steps.len() - committed,
                "final_hash": frame_hash,
            })
        );

        if let Some(prev) = &prev_hash {
            adjacent_pairs += 1;
            if *prev == frame_hash {
                adjacent_matches += 1;
            }
        }
        prev_hash = Some(frame_hash.clone());
        prev_dims = Some((cli.width, cli.height));
        final_hash = frame_hash;
        frames_completed += 1;

        if let Some(i) = outcome.halted_by_error {
            status = "failed";
            exit_code = 1;
            eprintln!(
                "error: pass index {i} failed at frame {f} and policy is `stop`; chain halted"
            );
            break;
        }
    }

    let rate = if adjacent_pairs == 0 {
        1.0
    } else {
        adjacent_matches as f64 / adjacent_pairs as f64
    };
    println!(
        "{}",
        json!({
            "type": "summary",
            "status": status,
            "frames_requested": cli.frames,
            "frames_completed": frames_completed,
            "adjacent_pairs": adjacent_pairs,
            "adjacent_hash_match_rate": rate,
            "final_sha256": final_hash,
        })
    );
    Ok(exit_code)
}
