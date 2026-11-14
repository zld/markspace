//! Chain manager: executes passes in order, keeps the committed chain
//! state, snapshots for rollback, propagates interrupts, and applies the
//! configured failure policy.
//!
//! Memory discipline: at any moment the chain holds only the committed
//! frame plus one candidate. No history frames are retained.

use crate::hash;
use crate::metrics::{self, PassMetrics};
use crate::pass::Pass;
use crate::Frame;

/// What to do when a pass fails.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum FailurePolicy {
    /// Roll back the failed pass and stop the chain.
    Stop,
    /// Roll back the failed pass, mark it, and continue with the next
    /// pass on the last committed frame. The failed frame is never
    /// fed into subsequent passes.
    RollbackContinue,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum StepStatus {
    Committed,
    FailedRolledBack,
}

/// Record of one attempted pass execution.
#[derive(Debug, Clone)]
pub struct StepRecord {
    pub index: usize,
    pub name: String,
    pub status: StepStatus,
    pub input_hash: String,
    pub output_hash: Option<String>,
    pub metrics: Option<PassMetrics>,
    pub error: Option<String>,
}

/// Result of running a chain over one source frame.
pub struct ChainOutcome {
    /// Last committed frame (the source frame if nothing committed).
    pub final_frame: Frame,
    pub steps: Vec<StepRecord>,
    /// Pass index before which an interrupt stopped the chain.
    pub interrupted_at: Option<usize>,
    /// Pass index whose failure halted the chain (policy = Stop).
    pub halted_by_error: Option<usize>,
}

/// Run `passes` over `source`.
///
/// `interrupt_before(i)` is polled before each pass; when it returns true
/// the chain stops, committed steps so far stay committed, and the current
/// step (never started, or fully rolled back) leaves no trace on the state.
pub fn run_chain(
    source: Frame,
    passes: &[Box<dyn Pass>],
    policy: FailurePolicy,
    interrupt_before: impl Fn(usize) -> bool,
) -> ChainOutcome {
    let mut current = source;
    let mut steps = Vec::with_capacity(passes.len());
    let mut interrupted_at = None;
    let mut halted_by_error = None;

    for (i, p) in passes.iter().enumerate() {
        if interrupt_before(i) {
            interrupted_at = Some(i);
            break;
        }

        let input_hash = hash::frame_hash(&current);
        // Snapshot: the pass works on an independent candidate; `current`
        // keeps the pre-pass state so a failure is a clean rollback.
        let candidate = current.clone();

        // A pass that changed dimensions counts as a failed pass too.
        let result = p
            .apply(&candidate)
            .and_then(|output| metrics::compare(&current, &output).map(|m| (output, m)));

        match result {
            Ok((output, m)) => {
                let output_hash = hash::frame_hash(&output);
                steps.push(StepRecord {
                    index: i,
                    name: p.name().to_string(),
                    status: StepStatus::Committed,
                    input_hash,
                    output_hash: Some(output_hash),
                    metrics: Some(m),
                    error: None,
                });
                current = output; // commit; the snapshot is dropped
            }
            Err(e) => {
                // Candidate discarded; `current` and prior statistics are
                // exactly what they were before this pass started.
                steps.push(StepRecord {
                    index: i,
                    name: p.name().to_string(),
                    status: StepStatus::FailedRolledBack,
                    input_hash,
                    output_hash: None,
                    metrics: None,
                    error: Some(e.to_string()),
                });
                match policy {
                    FailurePolicy::Stop => {
                        halted_by_error = Some(i);
                        break;
                    }
                    FailurePolicy::RollbackContinue => continue,
                }
            }
        }
    }

    ChainOutcome {
        final_frame: current,
        steps,
        interrupted_at,
        halted_by_error,
    }
}
