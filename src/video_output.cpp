#include "video_output.hpp"

#include <algorithm>
#include <chrono>

namespace avsync {

namespace {
constexpr int64_t kEarlyThresholdUs = 5'000;    // <=5ms early: show now
constexpr int64_t kDropThresholdUs = 40'000;    // >40ms late: drop+repeat
}  // namespace

VideoOutput::VideoOutput(BoundedQueue<VideoFrameMsg>& queue,
                         MasterClock& clock, Metrics& metrics,
                         int64_t frame_duration_us)
    : queue_(queue),
      clock_(clock),
      metrics_(metrics),
      sync_(kEarlyThresholdUs, kDropThresholdUs, frame_duration_us) {}

VideoOutput::~VideoOutput() { stop(); }

void VideoOutput::start(std::atomic<bool>& stop) {
    stop_flag_ = &stop;
    thread_ = std::thread(&VideoOutput::run, this);
    started_ = true;
}

void VideoOutput::stop() {
    if (started_ && thread_.joinable()) thread_.join();
    started_ = false;
}

void VideoOutput::sleep_interruptible_us(int64_t us) {
    auto deadline = std::chrono::steady_clock::now() +
                     std::chrono::microseconds(std::max<int64_t>(us, 0));
    while (!stop_flag_->load(std::memory_order_relaxed)) {
        auto now = std::chrono::steady_clock::now();
        if (now >= deadline) break;
        auto remain = std::chrono::duration_cast<std::chrono::microseconds>(
                          deadline - now).count();
        std::this_thread::sleep_for(
            std::chrono::microseconds(std::min<int64_t>(remain, 2'000)));
    }
}

void VideoOutput::record_show(const SyncDecision& d) {
    sync_.on_frame_shown(d.effective_pts_us);
    metrics_.inc_displayed();
    metrics_.add_av_offset_us(d.offset_us);
    metrics_.add_drift_us(clock_.drift_us());
}

void VideoOutput::run() {
    while (!stop_flag_->load(std::memory_order_relaxed)) {
        VideoFrameMsg msg;
        if (!queue_.pop(msg)) return;  // closed and drained: clean EOF

        // Wait for the audio path to anchor the shared clock. This makes
        // every offset sample meaningful even if video is decoded first.
        while (!clock_.running() &&
               !stop_flag_->load(std::memory_order_relaxed)) {
            if (queue_.closed()) return;
            std::this_thread::sleep_for(std::chrono::milliseconds(2));
        }
        if (stop_flag_->load(std::memory_order_relaxed)) return;

        SyncDecision d =
            sync_.decide(msg.pts_us, msg.pts_valid, clock_.now_us());
        if (d.pts_estimated) metrics_.inc_missing_pts();

        switch (d.action) {
            case FrameAction::kWait: {
                sleep_interruptible_us(d.wait_us);
                if (stop_flag_->load(std::memory_order_relaxed)) return;
                // Re-decide against the advanced clock: a long stall may have
                // turned an early frame into a late one.
                d = sync_.decide(msg.pts_us, msg.pts_valid, clock_.now_us());
                if (d.action == FrameAction::kDrop) {                    metrics_.inc_dropped();
                    if (sync_.has_last()) {
                        metrics_.inc_repeated();
                        metrics_.add_av_offset_us(d.offset_us);
                        metrics_.add_drift_us(clock_.drift_us());
                    }
                    break;
                }
                record_show(d);
                break;
            }
            case FrameAction::kShow:
                record_show(d);
                break;
            case FrameAction::kDrop:
                metrics_.inc_dropped();
                // Repeat the most recent frame to preserve output cadence.
                // Nothing can be repeated before the first presentation.
                if (sync_.has_last()) {
                    metrics_.inc_repeated();
                    metrics_.add_av_offset_us(d.offset_us);
                    metrics_.add_drift_us(clock_.drift_us());
                }
                break;
            case FrameAction::kRepeat:
                // Reserved by the controller contract; handled via kDrop.
                break;
        }
    }
}

}  // namespace avsync
