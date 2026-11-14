//! JSON chain configuration: parsing and validation.
//!
//! Validation covers parameter ranges (rejecting NaN and infinities),
//! pass-name uniqueness, pass-count limits and the dependency order of
//! pass kinds (linear-space operations before gamma). A configuration
//! that fails validation never starts processing.

use crate::chain::FailurePolicy;
use crate::pass::{BrightnessPass, GammaPass, Pass, SharpenPass};
use crate::{PipelineError, MAX_PASSES};
use serde::Deserialize;
use std::collections::HashSet;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Deserialize, Default)]
#[serde(rename_all = "snake_case")]
pub enum FailurePolicyConfig {
    /// Stop the chain at the first failed pass.
    #[default]
    Stop,
    /// Roll back the failed pass and continue with the next one.
    RollbackContinue,
}

#[derive(Debug, Deserialize)]
pub struct ChainConfig {
    #[serde(default)]
    pub on_pass_failure: FailurePolicyConfig,
    /// Optional explicit interrupt marker: the chain stops before the
    /// pass at this index (used for deterministic interruption tests).
    #[serde(default)]
    pub interrupt_before_pass: Option<usize>,
    pub passes: Vec<PassConfig>,
}

#[derive(Debug, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum PassConfig {
    Brightness { name: String, offset: f64 },
    Sharpen { name: String, amount: f64 },
    Gamma { name: String, gamma: f64 },
}

impl PassConfig {
    pub fn name(&self) -> &str {
        match self {
            PassConfig::Brightness { name, .. }
            | PassConfig::Sharpen { name, .. }
            | PassConfig::Gamma { name, .. } => name,
        }
    }

    /// Dependency rank: linear-space passes (brightness, then sharpen)
    /// must run before display-referred gamma correction.
    fn rank(&self) -> u8 {
        match self {
            PassConfig::Brightness { .. } => 0,
            PassConfig::Sharpen { .. } => 1,
            PassConfig::Gamma { .. } => 2,
        }
    }

    fn kind(&self) -> &'static str {
        match self {
            PassConfig::Brightness { .. } => "brightness",
            PassConfig::Sharpen { .. } => "sharpen",
            PassConfig::Gamma { .. } => "gamma",
        }
    }
}

impl ChainConfig {
    pub fn from_str(text: &str) -> Result<Self, PipelineError> {
        serde_json::from_str(text)
            .map_err(|e| PipelineError::Config(format!("JSON parse error: {e}")))
    }

    pub fn failure_policy(&self) -> FailurePolicy {
        match self.on_pass_failure {
            FailurePolicyConfig::Stop => FailurePolicy::Stop,
            FailurePolicyConfig::RollbackContinue => FailurePolicy::RollbackContinue,
        }
    }

    /// Validate everything that does not require building the passes.
    pub fn validate(&self) -> Result<(), PipelineError> {
        if self.passes.len() > MAX_PASSES {
            return Err(PipelineError::Config(format!(
                "too many passes: {} (max {MAX_PASSES})",
                self.passes.len()
            )));
        }

        let mut names = HashSet::with_capacity(self.passes.len());
        for p in &self.passes {
            let name = p.name();
            if name.trim().is_empty() {
                return Err(PipelineError::Config("pass name must not be empty".into()));
            }
            if !names.insert(name) {
                return Err(PipelineError::Config(format!(
                    "duplicate pass name `{name}`"
                )));
            }
        }

        // Dependency order: ranks must be non-decreasing along the chain.
        for w in self.passes.windows(2) {
            if w[1].rank() < w[0].rank() {
                return Err(PipelineError::Config(format!(
                    "pass dependency order violated: `{}` ({}) must not come after `{}` ({}); \
                     linear-space passes must precede gamma correction",
                    w[1].name(),
                    w[1].kind(),
                    w[0].name(),
                    w[0].kind()
                )));
            }
        }

        if let Some(i) = self.interrupt_before_pass {
            if i >= self.passes.len() {
                return Err(PipelineError::Config(format!(
                    "interrupt_before_pass {i} is out of range for {} passes",
                    self.passes.len()
                )));
            }
        }
        Ok(())
    }

    /// Build the pass objects; constructors re-check parameter ranges and
    /// reject NaN / infinite values.
    pub fn build_passes(&self) -> Result<Vec<Box<dyn Pass>>, PipelineError> {
        self.validate()?;
        let mut out: Vec<Box<dyn Pass>> = Vec::with_capacity(self.passes.len());
        for p in &self.passes {
            let built: Box<dyn Pass> = match p {
                PassConfig::Brightness { name, offset } => {
                    Box::new(BrightnessPass::new(name.clone(), *offset)?)
                }
                PassConfig::Sharpen { name, amount } => {
                    Box::new(SharpenPass::new(name.clone(), *amount)?)
                }
                PassConfig::Gamma { name, gamma } => {
                    Box::new(GammaPass::new(name.clone(), *gamma)?)
                }
            };
            out.push(built);
        }
        Ok(out)
    }
}
