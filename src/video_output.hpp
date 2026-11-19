#pragma once

#include <atomic>
#include <string>
#include <thread>

#include "bounded_queue.hpp"
#include "master_clock.hpp"
#include "media_source.hpp"
#include "metrics.hpp"
#include "sync_controller.hpp"

namespace avsync {

// Headless video output path. Every decoded frame is scheduled against the
// shared master clock via SyncController:
//   kWait   - sleep until the presentation time, then show
//   kShow   - present immediately
//   kDrop   - too late; count the drop and repeat the last shown frame so
//             the output cadence never stalls
// Missing/abnormal pts are estimated by the controller and counted here.
// The thread records av-offset and clock-drift samples for every decision.
class VideoOutput {
public:
    VideoOutput(BoundedQueue<VideoFrameMsg>& queue, MasterClock& clock,
                Metrics& metrics, int64_t frame_duration_us);
    ~VideoOutput();

    VideoOutput(const VideoOutput&) = delete;
    VideoOutput& operator=(const VideoOutput&) = delete;

    void start(std::atomic<bool>& stop);
    void stop();  // joins; the caller owns and closes the queue

    const std::string& error() const { return error_; }
    const SyncController& controller() const { return sync_; }

private:
    void run();
    void sleep_interruptible_us(int64_t us);
    void record_show(const SyncDecision& d);

    BoundedQueue<VideoFrameMsg>& queue_;
    MasterClock& clock_;
    Metrics& metrics_;
    SyncController sync_;
    std::atomic<bool>* stop_flag_ = nullptr;
    std::thread thread_;
    std::string error_;
    bool started_ = false;
};

}  // namespace avsync
