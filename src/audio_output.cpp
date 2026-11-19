#include "audio_output.hpp"

#include <algorithm>
#include <chrono>

namespace avsync {

namespace {
constexpr int64_t kUnderrunPollUs = 2'000;      // device wake-up granularity
constexpr int kInjectedUnderrunAtChunk = 48;    // ~40% into a 12s sample
}  // namespace

AudioOutput::AudioOutput(BoundedQueue<AudioChunk>& queue, MasterClock& clock,
                         Metrics& metrics, int injected_underrun_ms)
    : queue_(queue),
      clock_(clock),
      metrics_(metrics),
      injected_underrun_ms_(injected_underrun_ms) {}

AudioOutput::~AudioOutput() { stop(); }

void AudioOutput::start(std::atomic<bool>& stop) {
    stop_flag_ = &stop;
    thread_ = std::thread(&AudioOutput::run, this);
    started_ = true;
}

void AudioOutput::stop() {
    if (started_ && thread_.joinable()) thread_.join();
    started_ = false;
}

void AudioOutput::sleep_interruptible_us(int64_t us) {
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

void AudioOutput::run() {
    int64_t next_end_us = 0;   // expected device position of the next chunk
    int64_t chunks = 0;
    bool injected = false;

    while (!stop_flag_->load(std::memory_order_relaxed)) {
        AudioChunk chunk;
        bool timed_out = false;
        bool got = queue_.pop_for(chunk, std::chrono::milliseconds(20),
                                  timed_out);
        if (!got) {
            if (timed_out && !queue_.closed()) {
                // Natural underrun: the decoder could not feed the device.
                ++underruns_;
                metrics_.inc_underruns();
                clock_.pause();
                while (!stop_flag_->load(std::memory_order_relaxed)) {
                    bool again = false;
                    if (queue_.pop_for(chunk,
                            std::chrono::microseconds(kUnderrunPollUs), again))
                        break;
                    if (!again || queue_.closed()) return;  // drained EOF
                }
                if (stop_flag_->load(std::memory_order_relaxed)) return;
                // Resume at the actual media position: never jump backward.
                int64_t resume = chunk.pts_valid ? chunk.pts_us : next_end_us;
                clock_.resume();
                clock_.reset(resume);
                next_end_us = resume;
            } else {
                return;  // closed and drained: clean end of stream
            }
        }

        if (chunk.sample_rate <= 0 || chunk.nb_samples <= 0) {
            error_ = "audio chunk with invalid sample geometry";
            return;
        }
        const int64_t dur_us =
            static_cast<int64_t>(chunk.nb_samples) * 1'000'000 /
            chunk.sample_rate;
        const int64_t pts_us = chunk.pts_valid ? chunk.pts_us : next_end_us;

        if (chunks == 0) {
            // Anchor the running clock at the first chunk's media position
            // before any playback time elapses.
            clock_.start(pts_us);
            next_end_us = pts_us;
        }
        if (!injected && injected_underrun_ms_ > 0 &&
            chunks == kInjectedUnderrunAtChunk) {
            injected = true;
            ++underruns_;
            metrics_.inc_underruns();
            clock_.pause();
            sleep_interruptible_us(
                static_cast<int64_t>(injected_underrun_ms_) * 1000);
            if (stop_flag_->load(std::memory_order_relaxed)) return;
            // Re-anchor at the real position after the stall: no backward jump.
            clock_.resume();
            clock_.reset(pts_us);
            next_end_us = pts_us;
        }
        ++chunks;

        // Simulated device: block until this chunk finishes playing.
        sleep_interruptible_us(dur_us);
        if (stop_flag_->load(std::memory_order_relaxed)) return;

        // Consumed samples drive the master clock. set() re-anchors at the
        // exact media position; MasterClock guarantees monotonicity.
        next_end_us = pts_us + dur_us;
        clock_.set(next_end_us);
        metrics_.add_audio_samples(chunk.nb_samples);
        metrics_.add_drift_us(clock_.drift_us());
    }
}

}  // namespace avsync
