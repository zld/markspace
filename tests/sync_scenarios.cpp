// Unit scenarios for the self-built clock and the wait/show/drop/repeat
// controller. Everything is deterministic: no threads, no wall-time sleeps,
// only integer microsecond math. Exit code 4 on any failure.

#include <chrono>
#include <cstdio>
#include <cstdlib>
#include <thread>

#include "bounded_queue.hpp"
#include "master_clock.hpp"
#include "media_source.hpp"
#include "metrics.hpp"
#include "sync_controller.hpp"
#include "video_output.hpp"

namespace {

int g_failures = 0;
int g_checks = 0;

#define CHECK(cond)                                                     \
    do {                                                                \
        ++g_checks;                                                     \
        if (!(cond)) {                                                  \
            std::fprintf(stderr, "FAIL %s:%d: %s\n", __FILE__,          \
                         __LINE__, #cond);                              \
            ++g_failures;                                               \
        }                                                               \
    } while (0)

void test_clock_monotonic_across_state_changes() {
    avsync::MasterClock c;
    c.start(1'000'000);
    int64_t prev = c.now_us();
    // Repeated pause/resume, out-of-order calls and resets must never move
    // the returned media time backward.
    for (int i = 0; i < 50; ++i) {
        c.pause();
        c.pause();  // repeated: no-op, must not break anything
        c.reset(500'000 + i * 1000);  // anchor "earlier": still monotonic out
        c.resume();
        c.resume();
        int64_t now = c.now_us();
        CHECK(now >= prev);
        prev = now;
    }
}

void test_clock_pause_freezes_media_time() {
    avsync::MasterClock c;
    c.start(2'000'000);
    c.pause();
    int64_t a = c.now_us();
    // Busy-wait a little wall time; a frozen clock must not advance.
    for (volatile int i = 0; i < 100000; ++i) { }
    int64_t b = c.now_us();
    CHECK(b == a);
    c.resume();
    int64_t d1 = c.drift_us();
    // While paused the ideal timeline advanced, so drift is non-positive.
    CHECK(d1 <= 1000);  // tiny scheduling tolerance
}

void test_clock_reset_and_set_never_go_backward() {
    avsync::MasterClock c;
    c.start(10'000'000);
    c.set(9'000'000);  // earlier media position
    CHECK(c.now_us() >= 9'000'000 - 1);
    int64_t prev = c.now_us();
    c.reset(1'000);
    CHECK(c.now_us() >= prev);
}

void test_sync_wait_show_drop_thresholds() {
    constexpr int64_t early = 5'000;
    constexpr int64_t drop = 40'000;
    constexpr int64_t frame = 33'367;
    avsync::SyncController sc(early, drop, frame);

    // Frame 30ms in the future -> wait, capped, positive offset.
    auto d_wait = sc.decide(1'030'000, true, 1'000'000);
    CHECK(d_wait.action == avsync::FrameAction::kWait);
    CHECK(d_wait.wait_us > 0 && d_wait.wait_us <= sc.kMaxWaitUs);
    CHECK(d_wait.offset_us == 30'000);

    // Frame 2ms early -> within early threshold -> show now.
    auto d_show = sc.decide(1'002'000, true, 1'000'000);
    CHECK(d_show.action == avsync::FrameAction::kShow);

    // Frame 50ms late -> beyond drop threshold -> drop.
    auto d_drop = sc.decide(950'000, true, 1'000'000);
    CHECK(d_drop.action == avsync::FrameAction::kDrop);
    CHECK(d_drop.offset_us == -50'000);
}

void test_sync_missing_pts_is_estimated() {
    avsync::SyncController sc(5'000, 40'000, 33'367);
    // First frame, missing pts: estimate from the clock, flag it.
    auto d0 = sc.decide(0, false, 1'000'000);
    CHECK(d0.pts_estimated);
    CHECK(d0.effective_pts_us == 1'000'000);
    sc.on_frame_shown(d0.effective_pts_us);

    // A later missing pts is extrapolated one frame slot from the last shown.
    auto d1 = sc.decide(0, false, 1'100'000);
    CHECK(d1.pts_estimated);
    CHECK(d1.effective_pts_us == 1'000'000 + 33'367);
}

void test_sync_nonmonotonic_pts_is_rejected() {
    avsync::SyncController sc(5'000, 40'000, 33'367);
    auto d0 = sc.decide(1'000'000, true, 1'000'000);
    sc.on_frame_shown(d0.effective_pts_us);
    // A pts at or before the last shown one is treated as abnormal.
    auto d1 = sc.decide(1'000'000, true, 1'030'000);
    CHECK(d1.pts_estimated);
    CHECK(d1.effective_pts_us == 1'000'000 + 33'367);
}

void test_sync_absurd_pts_does_not_overflow() {
    avsync::SyncController sc(5'000, 40'000, 33'367);
    sc.on_frame_shown(1'000'000);
    auto d = sc.decide(INT64_MAX - 5, true, 1'000'000);
    CHECK(d.pts_estimated);  // absurd value rejected, no overflow/crash
}

void test_sync_wait_is_capped() {
    avsync::SyncController sc(5'000, 40'000, 33'367);
    auto d = sc.decide(1'000'000 + 60'000'000, true, 1'000'000);
    CHECK(d.action == avsync::FrameAction::kWait);
    CHECK(d.wait_us == sc.kMaxWaitUs);  // 60s future cannot stall the pipeline
}

void test_bounded_queue_close_wakes_blocked_consumer() {
    avsync::BoundedQueue<int> q(4);
    bool exited = false;
    std::thread t([&] {
        int v = 0;
        while (q.pop(v)) {}  // blocks on empty until close()
        exited = true;
    });
    std::this_thread::sleep_for(std::chrono::milliseconds(20));
    CHECK(!exited);          // still blocked, as expected
    q.close();
    t.join();
    CHECK(exited);           // close() woke and released the consumer
}

void test_bounded_queue_close_wakes_blocked_producer() {
    avsync::BoundedQueue<int> q(2);
    CHECK(q.push(1));
    CHECK(q.push(2));        // now full
    bool pushed = true;
    std::thread t([&] { pushed = q.push(3); });  // blocks: queue full
    std::this_thread::sleep_for(std::chrono::milliseconds(20));
    q.close();
    t.join();
    CHECK(!pushed);          // close() failed the blocked push
}

void test_bounded_queue_drains_before_close_returns_false() {
    avsync::BoundedQueue<int> q(4);
    q.push(10);
    q.push(20);
    q.close();
    int v = 0;
    CHECK(q.pop(v) && v == 10);
    CHECK(q.pop(v) && v == 20);
    CHECK(!q.pop(v));        // drained: now signals end
}

// End-to-end on the real video thread: once the clock runs ahead, valid
// monotonic frames arriving late must be dropped and the last shown frame
// repeated, with matching counters.
void test_video_thread_drops_late_and_repeats() {
    avsync::BoundedQueue<avsync::VideoFrameMsg> q(64);
    avsync::MasterClock clock;
    avsync::Metrics metrics;
    std::atomic<bool> stop{false};
    clock.start(1'000'000);
    avsync::VideoOutput video(q, clock, metrics, 33'367);
    video.start(stop);

    avsync::VideoFrameMsg f0;
    f0.index = 0;
    f0.pts_valid = true;
    f0.pts_us = 1'000'000;
    q.push(f0);
    std::this_thread::sleep_for(std::chrono::milliseconds(50));

    // Clock leaps ahead; subsequent frames are valid but >40ms late.
    clock.set(1'600'000);
    for (int i = 1; i <= 4; ++i) {
        avsync::VideoFrameMsg f;
        f.index = i;
        f.pts_valid = true;
        f.pts_us = 1'000'000 + static_cast<int64_t>(i) * 33'367;
        q.push(f);
    }
    std::this_thread::sleep_for(std::chrono::milliseconds(300));
    q.close();
    video.stop();

    CHECK(metrics.displayed() == 1);
    CHECK(metrics.dropped() == 4);
    CHECK(metrics.repeated() == 4);
    CHECK(metrics.av_offset().count == 5);  // one offset sample per decision
}

}  // namespace

int main() {
    test_clock_monotonic_across_state_changes();
    test_clock_pause_freezes_media_time();
    test_clock_reset_and_set_never_go_backward();
    test_sync_wait_show_drop_thresholds();
    test_sync_missing_pts_is_estimated();
    test_sync_nonmonotonic_pts_is_rejected();
    test_sync_absurd_pts_does_not_overflow();
    test_sync_wait_is_capped();
    test_bounded_queue_close_wakes_blocked_consumer();
    test_bounded_queue_close_wakes_blocked_producer();
    test_bounded_queue_drains_before_close_returns_false();
    test_video_thread_drops_late_and_repeats();

    if (g_failures == 0) {
        std::printf("sync_scenarios: all %d checks passed\n", g_checks);
        return 0;
    }
    std::fprintf(stderr, "sync_scenarios: %d/%d checks failed\n",
                 g_failures, g_checks);
    return 4;
}
