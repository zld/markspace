// Pure C++ unit tests for the FFmpeg-free core: timestamp normalization,
// master clock drift/reset and synchronizer decisions. Run under ctest.
#include <cassert>
#include <cmath>
#include <cstdio>
#include <cstdlib>

#include "clock.hpp"
#include "synchronizer.hpp"

using namespace pwsync;

namespace {

// NDEBUG-proof checks: plain assert() compiles out in Release, which would
// leave tested values unused and silently skip the validation.
void check(bool condition, const char* message) {
    if (!condition) {
        std::fprintf(stderr, "test failure: %s\n", message);
        std::abort();
    }
}

void checkNear(double actual, double expected, const char* message) {
    check(std::abs(actual - expected) < 1e-12, message);
}

void testNormalizerRepairsAndRollback() {
    StreamTimeBase tb{1, 30};
    TimestampNormalizer n(0.25, 1.0 / 30.0);
    check(!n.normalize(30, true, 1, tb).repaired, "first frame needs no repair");
    auto missing = n.normalize(0, false, 1, tb);
    check(missing.repaired, "missing PTS must be flagged as repaired");
    checkNear(missing.pts_seconds, 31.0 / 30.0, "repaired PTS uses previous duration");
    // Small rollback inside tolerance: clamped monotonic, no discontinuity.
    auto small = n.normalize(30, true, 1, tb);
    check(!small.discontinuity, "small rollback is not a discontinuity");
    checkNear(small.pts_seconds, 31.0 / 30.0, "small rollback clamps monotonic");
    // Large rollback: discontinuity.
    check(n.normalize(5, true, 1, tb).discontinuity, "large rollback detected");
    check(n.repairs() == 1 && n.discontinuities() == 1, "normalizer counters");
}

void testNormalizerForwardGap() {
    StreamTimeBase tb{1, 30000};
    TimestampNormalizer n(0.25, 1000.0 / 30000.0);
    n.normalize(59000, true, 1000, tb);
    // Jump packet carries a widened duration (14500); must still be detected
    // because classification uses the nominal period, not packet duration.
    assert(n.normalize(73500, true, 14500, tb).discontinuity);
}

void testClockDriftAndReset() {
    MasterClock clk(ClockMode::AudioMaster);
    clk.updateReference(1.0, 0.02322, false);
    assert(std::abs(clk.updateReference(1.02322, 0.02322, false)) < 1e-12);
    // Output running slow: PTS advances less than the nominal step.
    assert(clk.updateReference(1.04000, 0.02322, false) < 0.0);
    const double before = clk.nowSeconds();
    // Rollback inside tolerance never walks the clock backwards.
    clk.updateReference(before - 0.001, 0.02322, false);
    assert(clk.nowSeconds() == before);
    // Discontinuity re-anchors and counts a reset; no negative drift accrual.
    assert(clk.updateReference(5.0, 0.02322, true) == 0.0);
    assert(clk.resetCount() == 1);
    assert(clk.stats().max_abs_drift_ms > 0.0);
}

void testSynchronizerRepeatNeedsPreviousFrame() {
    MasterClock clk(ClockMode::Monotonic);
    SynchronizerConfig cfg;
    cfg.policy = LatePolicy::Repeat;
    cfg.late_threshold_seconds = 0.035;
    Synchronizer sync("repeat", cfg);
    clk.updateReference(1.0, 1.0 / 30.0, false);
    // First frame is already late: drop is mandatory, no virtual repeat.
    assert(sync.onVideoFrame(0.9, clk) == SyncAction::Drop);
    assert(sync.stats().dropped_frames == 1);
    assert(sync.stats().repeated_frames == 0);
    // After one presentable frame, a late frame may repeat instead.
    assert(sync.onVideoFrame(1.0, clk) == SyncAction::Present);
    assert(sync.onVideoFrame(0.95, clk) == SyncAction::Repeat);
    assert(sync.stats().repeated_frames == 1);
    sync.finalize();
    assert(sync.stats().av_offset.samples == 3);
    assert(sync.stats().av_offset.histogram.size() == kHistogramBinCount);
}

void testHistogramEdges() {
    MasterClock clk(ClockMode::Monotonic);
    SynchronizerConfig cfg;
    cfg.path_delay_seconds = 0.0;
    cfg.late_threshold_seconds = 100.0;  // never trigger drop/wait here
    Synchronizer sync("hist", cfg);
    clk.updateReference(0.0, 1.0, false);
    // -100 ms -> first bin; +100 ms -> last bin; 0 ms -> central bin 4.
    sync.onVideoFrame(-0.1, clk);
    sync.onVideoFrame(0.1, clk);
    sync.onVideoFrame(0.0, clk);
    check(sync.stats().av_offset.histogram[0] == 1, "-100ms lands in first bin");
    check(sync.stats().av_offset.histogram[kHistogramBinCount - 1] == 1,
          "+100ms lands in last bin");
    check(sync.stats().av_offset.histogram[5] == 1, "0ms lands in bin [0,10)");
    check(sync.stats().av_offset.histogram.size() == kHistogramBinCount,
          "fixed histogram bin count");
}

}  // namespace

int main() {
    testNormalizerRepairsAndRollback();
    testNormalizerForwardGap();
    testClockDriftAndReset();
    testSynchronizerRepeatNeedsPreviousFrame();
    testHistogramEdges();
    std::puts("core unit tests OK");
    return 0;
}
