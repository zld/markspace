#pragma once

#include <cstdint>
#include <mutex>

namespace avsync {

// Monotonic master clock in integer microseconds of media time.
//
// The clock free-runs off the wall clock while running, and is re-anchored
// by the audio path via set(). now_us() is guaranteed non-decreasing across
// pause/resume/reset/set calls, including repeated or out-of-order calls.
class MasterClock {
public:
    MasterClock();

    // Anchor the clock at media_us and start running. Also anchors the
    // ideal (wall-paced) media timeline used for drift measurement.
    void start(int64_t media_us);

    // Freeze the clock. No-op when already paused.
    void pause();

    // Continue from the frozen value. No-op when already running.
    void resume();

    // Re-anchor at media_us without disturbing run state. The returned
    // time never moves backward.
    void reset(int64_t media_us);

    // External master (audio consumption) drives the clock to media_us.
    void set(int64_t media_us);

    // Current media time in microseconds; monotonic non-decreasing.
    int64_t now_us() const;

    bool running() const;

    // now_us() minus the ideal wall-paced media time. Negative while the
    // clock is frozen (e.g. during an audio underrun).
    int64_t drift_us() const;

private:
    static int64_t wall_us();

    mutable std::mutex mu_;
    int64_t base_media_us_ = 0;    // media time at the last anchor
    int64_t base_wall_us_ = 0;     // wall time at the last anchor
    int64_t ideal_media_us_ = 0;   // media anchor of the ideal timeline
    int64_t ideal_wall_us_ = 0;    // wall anchor of the ideal timeline
    bool running_ = false;
    mutable int64_t last_returned_us_ = INT64_MIN;
};

}  // namespace avsync
