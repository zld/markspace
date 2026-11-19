#include "synchronizer.hpp"

#include <algorithm>
#include <cmath>

#include "clock.hpp"

namespace pwsync {

const char* toString(SyncAction action) {
    switch (action) {
        case SyncAction::Present:
            return "present";
        case SyncAction::Wait:
            return "wait";
        case SyncAction::Drop:
            return "drop";
        case SyncAction::Repeat:
            return "repeat";
    }
    return "present";
}

Synchronizer::Synchronizer(std::string path_name, SynchronizerConfig config)
    : config_(config) {
    stats_.name = std::move(path_name);
    stats_.delay_ms = config.path_delay_seconds * 1000.0;
}

void Synchronizer::recordOffset(double offset_seconds) {
    const double offset_ms = offset_seconds * 1000.0;
    auto& hist = stats_.av_offset.histogram;
    std::size_t bin = 0;
    for (std::size_t i = 0; i < kHistogramEdgesMs.size(); ++i) {
        if (offset_ms >= kHistogramEdgesMs[i]) {
            bin = i + 1;
        } else {
            break;
        }
    }
    ++hist[bin];
    offset_samples_ms_.push_back(offset_ms);
    stats_.av_offset.max_abs_ms =
        std::max(stats_.av_offset.max_abs_ms, std::abs(offset_ms));
    stats_.av_offset.samples++;
}

SyncAction Synchronizer::onVideoFrame(double frame_pts_seconds,
                                      const MasterClock& clock) {
    ++stats_.video_frames;
    // Positive offset: video is ahead of the master (early).
    // Negative offset: video is behind the master (late).
    const double offset =
        frame_pts_seconds - (clock.nowSeconds() + config_.path_delay_seconds);
    recordOffset(offset);

    if (offset > config_.late_threshold_seconds) {
        ++stats_.waits;
        return SyncAction::Wait;
    }
    if (offset < -config_.late_threshold_seconds) {
        if (config_.policy == LatePolicy::Repeat && has_previous_video_) {
            ++stats_.repeated_frames;
            return SyncAction::Repeat;
        }
        // Drop policy, or no frame available to repeat yet: never inflate the
        // repeat counter before the first presentable video frame.
        ++stats_.dropped_frames;
        return SyncAction::Drop;
    }
    has_previous_video_ = true;
    return SyncAction::Present;
}

void Synchronizer::onAudioFrame(double frame_pts_seconds,
                                const MasterClock& clock) {
    ++stats_.audio_frames;
    recordOffset(frame_pts_seconds -
                 (clock.nowSeconds() + config_.path_delay_seconds));
}

void Synchronizer::finalize() {
    auto& agg = stats_.av_offset;
    if (offset_samples_ms_.empty()) {
        agg.p50_ms = 0.0;
        agg.p95_ms = 0.0;
        agg.mean_ms = 0.0;
        return;
    }
    std::sort(offset_samples_ms_.begin(), offset_samples_ms_.end());
    const std::size_t n = offset_samples_ms_.size();
    // Nearest-rank percentiles on the signed offset distribution.
    auto percentile = [&](double p) -> double {
        std::size_t rank = static_cast<std::size_t>(
            std::ceil(p * static_cast<double>(n)));
        if (rank == 0) {
            rank = 1;
        }
        return offset_samples_ms_[rank - 1];
    };
    agg.p50_ms = percentile(0.50);
    agg.p95_ms = percentile(0.95);
    double sum = 0.0;
    for (double v : offset_samples_ms_) {
        sum += v;
    }
    agg.mean_ms = sum / static_cast<double>(n);
}

}  // namespace pwsync
