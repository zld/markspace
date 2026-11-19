#pragma once

#include <atomic>
#include <string>
#include <thread>

#include "bounded_queue.hpp"
#include "master_clock.hpp"
#include "media_source.hpp"
#include "metrics.hpp"

namespace avsync {

// Audio output path. In a headless kernel there is no audio device, so the
// "device" is simulated in wall time: each decoded chunk blocks for exactly
// its sample duration, then the consumed-sample position drives the shared
// master clock.
//
// Underruns (no chunk available, or an injected stall) freeze the clock and
// are counted; on recovery the clock is re-anchored at the new media position
// so the media timeline never jumps backward.
class AudioOutput {
public:
    AudioOutput(BoundedQueue<AudioChunk>& queue, MasterClock& clock,
                Metrics& metrics, int injected_underrun_ms);
    ~AudioOutput();

    AudioOutput(const AudioOutput&) = delete;
    AudioOutput& operator=(const AudioOutput&) = delete;

    // Launches the consumer thread.
    void start(std::atomic<bool>& stop);

    // Signals shutdown, closes nothing (the caller owns the queue) and joins.
    void stop();

    int64_t underruns() const { return underruns_; }
    const std::string& error() const { return error_; }

private:
    void run();
    void sleep_interruptible_us(int64_t us);

    BoundedQueue<AudioChunk>& queue_;
    MasterClock& clock_;
    Metrics& metrics_;
    const int injected_underrun_ms_;
    std::atomic<bool>* stop_flag_ = nullptr;
    std::thread thread_;
    std::atomic<int64_t> underruns_{0};
    std::string error_;
    bool started_ = false;
};

}  // namespace avsync
