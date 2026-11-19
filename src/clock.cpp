#include "clock.hpp"

#include <algorithm>
#include <stdexcept>

namespace pwsync {

ClockMode parseClockMode(const std::string& text) {
    if (text == "audio") {
        return ClockMode::AudioMaster;
    }
    if (text == "monotonic") {
        return ClockMode::Monotonic;
    }
    throw std::invalid_argument("clock mode must be 'audio' or 'monotonic', got: " + text);
}

TimestampNormalizer::TimestampNormalizer(double discontinuity_tolerance_seconds,
                                         double fallback_duration_seconds)
    : tolerance_seconds_(discontinuity_tolerance_seconds),
      fallback_duration_seconds_(fallback_duration_seconds > 0.0
                                     ? fallback_duration_seconds
                                     : 0.0) {}

TimestampNormalizer::Result TimestampNormalizer::normalize(
    std::int64_t pts_ticks, bool pts_valid,
    std::int64_t order_ticks, bool order_valid,
    std::int64_t duration_ticks, const StreamTimeBase& time_base) {
    Result out;
    const double tick = time_base.secondsPerTick();
    double pts_seconds = pts_valid ? static_cast<double>(pts_ticks) * tick : 0.0;

    // Missing presentation PTS: infer from the previous frame's duration.
    if (!pts_valid) {
        out.repaired = true;
        ++repairs_;
        pts_seconds = initialized_ ? last_pts_seconds_ + last_duration_seconds_ : 0.0;
    }

    // Discontinuity classification runs on decode-order timestamps so B-frame
    // PTS reordering is not mistaken for a rollback. Fall back to PTS only if
    // the container provides no order timestamp.
    const double order_seconds =
        order_valid ? static_cast<double>(order_ticks) * tick : pts_seconds;
    if (initialized_) {
        const double delta = order_seconds - last_order_seconds_;
        const bool backward = delta < -tolerance_seconds_;
        const bool forward_gap =
            delta > fallback_duration_seconds_ + tolerance_seconds_ &&
            delta > tolerance_seconds_;
        if (backward || forward_gap) {
            out.discontinuity = true;
            ++discontinuities_;
        }
    }

    double duration_seconds =
        duration_ticks > 0 ? static_cast<double>(duration_ticks) * tick
                           : fallback_duration_seconds_;
    if (duration_seconds < 0.0) {
        duration_seconds = fallback_duration_seconds_;
    }

    out.pts_seconds = pts_seconds;
    out.duration_seconds = duration_seconds;

    initialized_ = true;
    last_pts_seconds_ = pts_seconds;
    last_order_seconds_ = order_seconds;
    last_duration_seconds_ = duration_seconds;
    return out;
}

MasterClock::MasterClock(ClockMode mode) : mode_(mode) {}

void MasterClock::reset() {
    anchored_ = false;
    actual_seconds_ = 0.0;
    nominal_seconds_ = 0.0;
    samples_ = 0;
    drift_sum_ms_ = 0.0;
    max_abs_drift_ms_ = 0.0;
    ++resets_;
}

void MasterClock::recordDrift(double drift_seconds) {
    const double drift_ms = drift_seconds * 1000.0;
    ++samples_;
    drift_sum_ms_ += drift_ms;
    max_abs_drift_ms_ = std::max(max_abs_drift_ms_, std::abs(drift_ms));
}

double MasterClock::updateReference(double pts_seconds, double nominal_step_seconds,
                                    bool discontinuity) {
    if (!anchored_ || discontinuity) {
        // First anchor, or a reported discontinuity: re-baseline both
        // timelines so no negative/invalid duration feeds the drift.
        const bool reanchor = anchored_;
        actual_seconds_ = pts_seconds;
        nominal_seconds_ = pts_seconds;
        anchored_ = true;
        if (reanchor) {
            ++resets_;
        }
        recordDrift(0.0);
        return 0.0;
    }

    if (pts_seconds < actual_seconds_) {
        // Rollback smaller than the normalizer's tolerance: hold the clock
        // instead of walking backwards.
        recordDrift(actual_seconds_ - nominal_seconds_);
        return actual_seconds_ - nominal_seconds_;
    }

    actual_seconds_ = pts_seconds;
    nominal_seconds_ += nominal_step_seconds > 0.0 ? nominal_step_seconds : 0.0;
    const double drift = actual_seconds_ - nominal_seconds_;
    recordDrift(drift);
    return drift;
}

ClockStats MasterClock::stats() const {
    ClockStats out;
    out.mode = (mode_ == ClockMode::AudioMaster) ? "audio" : "monotonic";
    out.samples = samples_;
    out.resets = resets_;
    out.cumulative_drift_ms = drift_sum_ms_;
    out.max_abs_drift_ms = max_abs_drift_ms_;
    out.mean_drift_ms = samples_ > 0 ? drift_sum_ms_ / static_cast<double>(samples_) : 0.0;
    return out;
}

}  // namespace pwsync
