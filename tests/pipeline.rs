//! Integration tests with deterministic, code-generated fixtures.
//! No external assets are read.

use postpipe::chain::{self, FailurePolicy, StepStatus};
use postpipe::config::ChainConfig;
use postpipe::hash;
use postpipe::image_source;
use postpipe::metrics;
use postpipe::pass::{BrightnessPass, GammaPass, Pass, SharpenPass};
use postpipe::{Frame, PipelineError};

// ---------- fixtures ----------

fn frame_2x2() -> Frame {
    // RGBA pixels with distinct values and a non-uniform alpha pattern.
    Frame::from_raw(
        2,
        2,
        vec![
            10, 20, 30, 255, //
            40, 50, 60, 128, //
            70, 80, 90, 255, //
            100, 110, 120, 0,
        ],
    )
    .unwrap()
}

struct FailPass {
    name: String,
}

impl Pass for FailPass {
    fn name(&self) -> &str {
        &self.name
    }
    fn apply(&self, _input: &Frame) -> Result<Frame, PipelineError> {
        Err(PipelineError::PassFailed {
            name: self.name.clone(),
            message: "synthetic failure".into(),
        })
    }
}

fn boxed<P: Pass + 'static>(p: P) -> Box<dyn Pass> {
    Box::new(p)
}

// ---------- passes ----------

#[test]
fn brightness_shifts_rgb_and_preserves_alpha() {
    let src = frame_2x2();
    let p = BrightnessPass::new("b", 10.0).unwrap();
    let out = p.apply(&src).unwrap();
    let raw = out.as_raw();
    assert_eq!(&raw[0..4], &[20, 30, 40, 255]);
    assert_eq!(&raw[4..8], &[50, 60, 70, 128]);

    let m = metrics::compare(&src, &out).unwrap();
    assert_eq!(m.total_abs_diff, 4 * 3 * 10);
    assert_eq!(m.changed_pixels, 4);
    assert_eq!(m.max_channel_diff, 10);
    assert_eq!(m.mean_abs_diff, 10.0);
    assert_eq!(m.alpha_changed_pixels, 0);
}

#[test]
fn brightness_clamps_and_rounds() {
    let src = Frame::from_raw(1, 2, vec![250, 5, 0, 255, 100, 100, 100, 255]).unwrap();
    let up = BrightnessPass::new("b", 10.0).unwrap().apply(&src).unwrap();
    assert_eq!(&up.as_raw()[0..3], &[255, 15, 10]);
    let down = BrightnessPass::new("b", -10.6).unwrap().apply(&src).unwrap();
    // -10.6 rounds per channel: 250-10.6=239.4 -> 239, 5-10.6 -> 0 (clamped)
    assert_eq!(&down.as_raw()[0..3], &[239, 0, 0]);
}

#[test]
fn sharpen_is_identity_on_1x1() {
    let src = Frame::from_raw(1, 1, vec![37, 200, 90, 255]).unwrap();
    let out = SharpenPass::new("s", 1.5).unwrap().apply(&src).unwrap();
    assert_eq!(out.as_raw(), src.as_raw());
    let m = metrics::compare(&src, &out).unwrap();
    assert_eq!(m.total_abs_diff, 0);
    assert_eq!(m.changed_pixels, 0);
}

#[test]
fn sharpen_handles_1xn_and_nx1() {
    for (w, h) in [(1u32, 7u32), (7, 1)] {
        let src = image_source::generate_frame(9, 0, w, h).unwrap();
        let out = SharpenPass::new("s", 0.8).unwrap().apply(&src).unwrap();
        assert_eq!((out.width(), out.height()), (w, h));
        // alpha untouched
        assert!(out.as_raw().chunks_exact(4).all(|p| p[3] == 255));
    }
}

#[test]
fn sharpen_uses_edge_replication() {
    // 1x3 row [0, 100, 200], amount 1.0.
    // Leftmost pixel: neighbours clamp to itself: v = 0*5 - (0 + 100 + 0 + 0) = -100 -> 0.
    // Middle: 100*5 - (0 + 200 + 100 + 100) = 100.
    // Rightmost: 200*5 - (100 + 200 + 200 + 200) = 300 -> 255.
    let src = Frame::from_raw(3, 1, vec![0, 0, 0, 255, 100, 100, 100, 255, 200, 200, 200, 255])
        .unwrap();
    let out = SharpenPass::new("s", 1.0).unwrap().apply(&src).unwrap();
    let raw = out.as_raw();
    assert_eq!(raw[0], 0);
    assert_eq!(raw[4], 100);
    assert_eq!(raw[8], 255);
}

#[test]
fn passes_reject_zero_sized_frames() {
    let zero = Frame::from_raw(0, 0, vec![]).unwrap();
    assert!(matches!(
        SharpenPass::new("s", 1.0).unwrap().apply(&zero),
        Err(PipelineError::ZeroSizedFrame)
    ));
    assert!(matches!(
        BrightnessPass::new("b", 1.0).unwrap().apply(&zero),
        Err(PipelineError::ZeroSizedFrame)
    ));
    assert!(matches!(
        GammaPass::new("g", 2.2).unwrap().apply(&zero),
        Err(PipelineError::ZeroSizedFrame)
    ));
}

#[test]
fn from_raw_rejects_bad_channel_layout() {
    assert!(matches!(
        Frame::from_raw(2, 2, vec![0u8; 12]), // RGB, not RGBA
        Err(PipelineError::UnsupportedChannelLayout(_))
    ));
    assert!(matches!(
        Frame::from_raw(2, 2, vec![0u8; 17]),
        Err(PipelineError::UnsupportedChannelLayout(_))
    ));
}

#[test]
fn gamma_endpoints_and_monotonicity() {
    let g = GammaPass::new("g", 2.2).unwrap();
    let src = Frame::from_raw(
        4,
        1,
        vec![0, 64, 128, 255, 192, 255, 32, 255, 96, 160, 224, 255, 16, 48, 80, 255],
    )
    .unwrap();
    let out = g.apply(&src).unwrap();
    assert_eq!(out.as_raw()[0], 0); // 0 stays 0
    assert_eq!(out.as_raw()[5], 255); // 255 stays 255
    // gamma > 1 brightens mid-tones
    assert!(out.as_raw()[2] > 128);
    // alpha preserved
    assert!(out.as_raw().chunks_exact(4).all(|p| p[3] == 255));
}

// ---------- chain state, rollback, interrupt ----------

#[test]
fn rollback_continue_marks_failure_and_excludes_failed_frame() {
    let src = image_source::generate_frame(7, 0, 8, 8).unwrap();
    let passes: Vec<Box<dyn Pass>> = vec![
        boxed(BrightnessPass::new("up1", 10.0).unwrap()),
        boxed(FailPass { name: "boom".into() }),
        boxed(BrightnessPass::new("up2", 10.0).unwrap()),
    ];
    let outcome = chain::run_chain(src.clone(), &passes, FailurePolicy::RollbackContinue, |_| false);

    assert_eq!(outcome.steps.len(), 3);
    assert_eq!(outcome.steps[1].status, StepStatus::FailedRolledBack);
    assert!(outcome.steps[1].error.is_some());
    assert!(outcome.halted_by_error.is_none());

    // The pass after the failure ran on the last committed frame:
    // its input hash equals the first pass's output hash.
    assert_eq!(
        outcome.steps[2].input_hash,
        outcome.steps[1].input_hash // chain state unchanged by the failed pass
    );
    assert_eq!(
        outcome.steps[2].input_hash,
        outcome.steps[0].output_hash.clone().unwrap()
    );

    // Final frame equals the chain without the failing pass.
    let clean: Vec<Box<dyn Pass>> = vec![
        boxed(BrightnessPass::new("up1", 10.0).unwrap()),
        boxed(BrightnessPass::new("up2", 10.0).unwrap()),
    ];
    let expected = chain::run_chain(src, &clean, FailurePolicy::Stop, |_| false);
    assert_eq!(
        hash::frame_hash(&outcome.final_frame),
        hash::frame_hash(&expected.final_frame)
    );
}

#[test]
fn stop_policy_halts_and_preserves_pre_failure_state() {
    let src = image_source::generate_frame(7, 0, 8, 8).unwrap();
    let passes: Vec<Box<dyn Pass>> = vec![
        boxed(BrightnessPass::new("up1", 10.0).unwrap()),
        boxed(FailPass { name: "boom".into() }),
        boxed(BrightnessPass::new("up2", 10.0).unwrap()),
    ];
    let outcome = chain::run_chain(src, &passes, FailurePolicy::Stop, |_| false);

    assert_eq!(outcome.steps.len(), 2);
    assert_eq!(outcome.halted_by_error, Some(1));
    // Final state is the committed output of pass 0.
    assert_eq!(
        hash::frame_hash(&outcome.final_frame),
        outcome.steps[0].output_hash.clone().unwrap()
    );
}

#[test]
fn interrupt_stops_remaining_passes_and_keeps_committed_steps() {
    let src = image_source::generate_frame(7, 0, 8, 8).unwrap();
    let passes: Vec<Box<dyn Pass>> = vec![
        boxed(BrightnessPass::new("up1", 10.0).unwrap()),
        boxed(BrightnessPass::new("up2", 10.0).unwrap()),
        boxed(GammaPass::new("g", 2.2).unwrap()),
    ];
    let outcome = chain::run_chain(src, &passes, FailurePolicy::Stop, |i| i == 1);

    assert_eq!(outcome.interrupted_at, Some(1));
    assert_eq!(outcome.steps.len(), 1);
    assert_eq!(outcome.steps[0].status, StepStatus::Committed);
    assert_eq!(
        hash::frame_hash(&outcome.final_frame),
        outcome.steps[0].output_hash.clone().unwrap()
    );
}

// ---------- determinism & frame sequences ----------

#[test]
fn same_seed_config_and_sequence_give_identical_results() {
    let passes: Vec<Box<dyn Pass>> = vec![
        boxed(BrightnessPass::new("lift", 12.0).unwrap()),
        boxed(SharpenPass::new("crisp", 0.6).unwrap()),
        boxed(GammaPass::new("display", 2.2).unwrap()),
    ];
    let run = || -> Vec<(String, Vec<u64>)> {
        (0..4)
            .map(|f| {
                let src = image_source::generate_frame(41, f, 32, 24).unwrap();
                let out = chain::run_chain(src, &passes, FailurePolicy::Stop, |_| false);
                (
                    hash::frame_hash(&out.final_frame),
                    out.steps.iter().filter_map(|s| s.metrics).map(|m| m.total_abs_diff).collect(),
                )
            })
            .collect()
    };
    assert_eq!(run(), run());
}

#[test]
fn consecutive_frames_differ_but_are_stable() {
    let h0 = hash::frame_hash(&image_source::generate_frame(41, 0, 16, 16).unwrap());
    let h1 = hash::frame_hash(&image_source::generate_frame(41, 1, 16, 16).unwrap());
    let h0_again = hash::frame_hash(&image_source::generate_frame(41, 0, 16, 16).unwrap());
    assert_ne!(h0, h1);
    assert_eq!(h0, h0_again);
}

#[test]
fn source_validates_dimensions_and_frame_index() {
    assert!(matches!(
        image_source::generate_frame(0, 0, 0, 10),
        Err(PipelineError::InvalidDimensions { .. })
    ));
    assert!(matches!(
        image_source::generate_frame(0, 0, 4097, 10),
        Err(PipelineError::InvalidDimensions { .. })
    ));
    assert!(matches!(
        image_source::generate_frame(0, 1000, 10, 10),
        Err(PipelineError::InvalidFrameIndex(1000))
    ));
    assert!(image_source::generate_frame(0, 999, 4096, 1).is_ok());
}

#[test]
fn boundary_sizes_1x1_1xn_nx1_generate_and_process() {
    for (w, h) in [(1u32, 1u32), (1, 5), (5, 1)] {
        let src = image_source::generate_frame(3, 0, w, h).unwrap();
        let passes: Vec<Box<dyn Pass>> = vec![
            boxed(BrightnessPass::new("b", 5.0).unwrap()),
            boxed(SharpenPass::new("s", 1.0).unwrap()),
            boxed(GammaPass::new("g", 1.8).unwrap()),
        ];
        let out = chain::run_chain(src, &passes, FailurePolicy::Stop, |_| false);
        assert_eq!(out.steps.len(), 3);
        assert!(out.steps.iter().all(|s| s.status == StepStatus::Committed));
        assert_eq!((out.final_frame.width(), out.final_frame.height()), (w, h));
    }
}

// ---------- metrics & hashing ----------

#[test]
fn metrics_use_wide_counters_and_report_alpha_separately() {
    let before = Frame::from_raw(2, 2, vec![0, 0, 0, 255, 0, 0, 0, 255, 0, 0, 0, 255, 0, 0, 0, 255]).unwrap();
    let mut after_data = vec![255u8; 16];
    after_data[3] = 0; // one alpha change
    let after = Frame::from_raw(2, 2, after_data).unwrap();

    let m = metrics::compare(&before, &after).unwrap();
    assert_eq!(m.total_abs_diff, 4 * 3 * 255);
    assert_eq!(m.changed_pixels, 4);
    assert_eq!(m.max_channel_diff, 255);
    assert_eq!(m.mean_abs_diff, 255.0);
    assert_eq!(m.alpha_changed_pixels, 1);
}

#[test]
fn metrics_reject_dimension_mismatch() {
    let a = Frame::from_raw(2, 2, vec![0u8; 16]).unwrap();
    let b = Frame::from_raw(4, 1, vec![0u8; 16]).unwrap();
    assert!(matches!(
        metrics::compare(&a, &b),
        Err(PipelineError::DimensionMismatch { .. })
    ));
}

#[test]
fn hash_is_stable_and_dimension_sensitive() {
    let a = Frame::from_raw(2, 2, vec![1u8; 16]).unwrap();
    let b = Frame::from_raw(4, 1, vec![1u8; 16]).unwrap();
    assert_eq!(hash::frame_hash(&a), hash::frame_hash(&a));
    assert_eq!(hash::frame_hash(&a).len(), 64);
    assert_ne!(hash::frame_hash(&a), hash::frame_hash(&b));
}

// ---------- configuration ----------

#[test]
fn config_accepts_valid_chain() {
    let cfg = ChainConfig::from_str(
        r#"{
            "on_pass_failure": "rollback_continue",
            "passes": [
                {"type": "brightness", "name": "lift", "offset": 12.0},
                {"type": "sharpen", "name": "crisp", "amount": 0.6},
                {"type": "gamma", "name": "display", "gamma": 2.2}
            ]
        }"#,
    )
    .unwrap();
    assert!(cfg.validate().is_ok());
    assert_eq!(cfg.build_passes().unwrap().len(), 3);
}

#[test]
fn config_rejects_nan_and_infinity() {
    // 1e999 overflows f64: rejected either at JSON parse or at validation.
    let bad = r#"{"passes": [{"type": "brightness", "name": "b", "offset": 1e999}]}"#;
    match ChainConfig::from_str(bad) {
        Err(PipelineError::Config(_)) => {}
        Ok(cfg) => assert!(matches!(cfg.build_passes(), Err(PipelineError::Config(_)))),
        Err(e) => panic!("unexpected error: {e}"),
    }

    let zero_gamma = r#"{"passes": [{"type": "gamma", "name": "g", "gamma": 0.0}]}"#;
    let cfg = ChainConfig::from_str(zero_gamma).unwrap();
    assert!(matches!(cfg.build_passes(), Err(PipelineError::Config(_))));
}

#[test]
fn config_rejects_out_of_range_coefficients() {
    let cases = [
        r#"{"passes": [{"type": "brightness", "name": "b", "offset": 300.0}]}"#,
        r#"{"passes": [{"type": "sharpen", "name": "s", "amount": -0.5}]}"#,
        r#"{"passes": [{"type": "sharpen", "name": "s", "amount": 9.0}]}"#,
        r#"{"passes": [{"type": "gamma", "name": "g", "gamma": 100.0}]}"#,
    ];
    for c in cases {
        let cfg = ChainConfig::from_str(c).unwrap();
        assert!(cfg.build_passes().is_err(), "should reject: {c}");
    }
}

#[test]
fn config_rejects_duplicate_names_and_empty_names() {
    let dup = r#"{"passes": [
        {"type": "brightness", "name": "x", "offset": 1.0},
        {"type": "brightness", "name": "x", "offset": 2.0}
    ]}"#;
    assert!(matches!(
        ChainConfig::from_str(dup).unwrap().validate(),
        Err(PipelineError::Config(_))
    ));

    let empty = r#"{"passes": [{"type": "brightness", "name": "  ", "offset": 1.0}]}"#;
    assert!(ChainConfig::from_str(empty).unwrap().validate().is_err());
}

#[test]
fn config_enforces_pass_dependency_order() {
    // gamma before a linear-space pass is rejected
    let bad = r#"{"passes": [
        {"type": "gamma", "name": "g", "gamma": 2.2},
        {"type": "brightness", "name": "b", "offset": 1.0}
    ]}"#;
    assert!(matches!(
        ChainConfig::from_str(bad).unwrap().validate(),
        Err(PipelineError::Config(_))
    ));
    // sharpen after brightness is fine
    let ok = r#"{"passes": [
        {"type": "brightness", "name": "b", "offset": 1.0},
        {"type": "sharpen", "name": "s", "amount": 1.0}
    ]}"#;
    assert!(ChainConfig::from_str(ok).unwrap().validate().is_ok());
}

#[test]
fn config_rejects_too_many_passes_and_bad_interrupt() {
    let mut passes = String::new();
    for i in 0..65 {
        if i > 0 {
            passes.push(',');
        }
        passes.push_str(&format!(
            r#"{{"type": "brightness", "name": "b{i}", "offset": 1.0}}"#
        ));
    }
    let text = format!(r#"{{"passes": [{passes}]}}"#);
    assert!(matches!(
        ChainConfig::from_str(&text).unwrap().validate(),
        Err(PipelineError::Config(_))
    ));

    let bad_interrupt =
        r#"{"interrupt_before_pass": 5, "passes": [{"type": "gamma", "name": "g", "gamma": 2.2}]}"#;
    assert!(ChainConfig::from_str(bad_interrupt).unwrap().validate().is_err());
}

#[test]
fn config_rejects_malformed_json_and_unknown_pass_type() {
    assert!(matches!(
        ChainConfig::from_str("{not json"),
        Err(PipelineError::Config(_))
    ));
    assert!(matches!(
        ChainConfig::from_str(r#"{"passes": [{"type": "blur", "name": "b"}]}"#),
        Err(PipelineError::Config(_))
    ));
}

#[test]
fn config_driven_chain_runs_end_to_end() {
    let cfg = ChainConfig::from_str(
        r#"{
            "on_pass_failure": "rollback_continue",
            "passes": [
                {"type": "brightness", "name": "lift", "offset": 12.0},
                {"type": "sharpen", "name": "crisp", "amount": 0.6},
                {"type": "gamma", "name": "display", "gamma": 2.2}
            ]
        }"#,
    )
    .unwrap();
    let passes = cfg.build_passes().unwrap();
    let src = image_source::generate_frame(41, 0, 32, 24).unwrap();
    let outcome = chain::run_chain(src, &passes, cfg.failure_policy(), |_| false);
    assert_eq!(outcome.steps.len(), 3);
    assert!(outcome.steps.iter().all(|s| s.status == StepStatus::Committed));
    assert!(outcome.steps.iter().all(|s| s.metrics.unwrap().alpha_changed_pixels == 0));
}
