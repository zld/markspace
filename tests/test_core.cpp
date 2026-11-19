// Pure C++ unit tests for the FFmpeg-free core: timestamp normalization,
// master clock drift/reset and synchronizer decisions. Run under ctest.
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
    check(!n.normalize(30, true, 30, true, 1, tb).repaired,
          "first frame needs no repair");
    auto missing = n.normalize(0, false, 31, true, 1, tb);
    check(missing.repaired, "missing PTS must be flagged as repaired");
    // Frame 0 at 30 ticks; missing frame inherits last duration (1 tick) -> 31.
    checkNear(missing.pts_seconds, 31.0 / 30.0,
              "repaired PTS uses previous duration");
    // Small rollback inside tolerance: no discontinuity.
    check(!n.normalize(30, true, 30, true, 1, tb).discontinuity,
          "small rollback is not a discontinuity");
    // Large rollback on the decode-order timestamp: discontinuity.
    check(n.normalize(5, true, 5, true, 1, tb).discontinuity,
          "large rollback detected");
    check(n.repairs() == 1 && n.discontinuities() == 1, "normalizer counters");
}

void testNormalizerForwardGap() {
    StreamTimeBase tb{1, 30000};
    TimestampNormalizer n(0.25, 1000.0 / 30000.0);
    n.normalize(59000, true, 59000, true, 1000, tb);
    // Jump packet carries a widened duration (14500); must still be detected
    // because classification uses the nominal period, not packet duration.
    check(n.normalize(73500, true, 73500, true, 14500, tb).discontinuity,
          "forward gap detected despite widened packet duration");
}

void testNormalizerBframeReordering() {
    // B frames arrive in DTS order; PTS may legitimately move backwards in
    // arrival order. Decode-order (DTS) stays monotonic, so no discontinuity.
    StreamTimeBase tb{1, 3000};
    TimestampNormalizer n(0.25, 100.0 / 3000.0);
    // DTS strictly advances one tick per packet; PTS reorders around it.
    const long pts[6] = {0, 1024, 512, 1536, 2048, 2560};
    const long dts[6] = {0, 512, 1024, 1536, 2048, 2560};
    for (int i = 0; i < 6; ++i) {
        auto r = n.normalize(pts[i], true, dts[i], true, 512, tb);
        check(!r.discontinuity, "B-frame PTS reordering must not flag a jump");
    }
    check(n.discontinuities() == 0, "no discontinuities from codec reordering");
}

void testClockDriftAndReset() {
    MasterClock clk(ClockMode::AudioMaster);
    clk.updateReference(1.0, 0.02322, false);
    check(std::abs(clk.updateReference(1.02322, 0.02322, false)) < 1e-12,
          "nominal step produces zero drift");
    // Output running slow: PTS advances less than the nominal step.
    check(clk.updateReference(1.04000, 0.02322, false) < 0.0,
          "slow output produces negative drift");
    const double before = clk.nowSeconds();
    // Rollback inside tolerance never walks the clock backwards.
    clk.updateReference(before - 0.001, 0.02322, false);
    check(clk.nowSeconds() == before, "clock never walks backwards");
    // Discontinuity re-anchors and counts a reset; no negative drift accrual.
    check(clk.updateReference(5.0, 0.02322, true) == 0.0,
          "discontinuity re-anchors at zero drift");
    check(clk.resetCount() == 1, "discontinuity counts one reset");
    check(clk.stats().max_abs_drift_ms > 0.0, "drift maximum was recorded");
}

void testSynchronizerRepeatNeedsPreviousFrame() {
    MasterClock clk(ClockMode::Monotonic);
    SynchronizerConfig cfg;
    cfg.policy = LatePolicy::Repeat;
    cfg.late_threshold_seconds = 0.035;
    Synchronizer sync("repeat", cfg);
    clk.updateReference(1.0, 1.0 / 30.0, false);
    // First frame is already late: drop is mandatory, no virtual repeat.
    check(sync.onVideoFrame(0.9, clk) == SyncAction::Drop, "first late frame drops");
    check(sync.stats().dropped_frames == 1, "one drop counted");
    check(sync.stats().repeated_frames == 0, "no repeat before any present frame");
    // After one presentable frame, a late frame may repeat instead.
    check(sync.onVideoFrame(1.0, clk) == SyncAction::Present, "on-time frame presents");
    check(sync.onVideoFrame(0.95, clk) == SyncAction::Repeat, "late frame repeats");
    check(sync.stats().repeated_frames == 1, "one repeat counted");
    sync.finalize();
    check(sync.stats().av_offset.samples == 3, "three offset samples");
    check(sync.stats().av_offset.histogram.size() == kHistogramBinCount,
          "fixed histogram bin count");
}

void testHistogramEdges() {
    MasterClock clk(ClockMode::Monotonic);
    SynchronizerConfig cfg;
    cfg.path_delay_seconds = 0.0;
    cfg.late_threshold_seconds = 100.0;  // never trigger drop/wait here
    Synchronizer sync("hist", cfg);
    clk.updateReference(0.0, 1.0, false);
    // -100 ms -> first bin; +100 ms -> last bin; 0 ms -> bin 5 ([0,10)).
    sync.onVideoFrame(-0.1, clk);
    sync.onVideoFrame(0.1, clk);
    sync.onVideoFrame(0.0, clk);
    check(sync.stats().av_offset.histogram[0] == 1, "-100ms lands in first bin");
    check(sync.stats().av_offset.histogram[kHistogramBinCount - 1] == 1,
          "+100ms lands in last bin");
    check(sync.stats().av_offset.histogram[5] == 1, "0ms lands in bin [0,10)");
}

}  // namespace

int main() {
    testNormalizerRepairsAndRollback();
    testNormalizerForwardGap();
    testNormalizerBframeReordering();
    testClockDriftAndReset();
    testSynchronizerRepeatNeedsPreviousFrame();
    testHistogramEdges();
    std::puts("core unit tests OK");
    return 0;
}
