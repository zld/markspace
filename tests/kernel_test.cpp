// State-level tests for the synchronization kernel. These exercise the
// deterministic, non-FFmpeg-IO components (clock, timestamp normalization,
// sync decisions, bounded queue, jitter) so the scheduling logic can be
// validated without a media file or worker threads.

#include <atomic>
#include <cassert>
#include <chrono>
#include <cstdio>
#include <string>
#include <thread>

extern "C" {
#include <libavutil/rational.h>
}

#include "avsync/clock.hpp"
#include "avsync/common.hpp"
#include "avsync/queue.hpp"
#include "avsync/sync.hpp"

namespace {
int g_failures = 0;

#define CHECK(cond)                                                       \
  do {                                                                     \
    if (!(cond)) {                                                         \
      std::fprintf(stderr, "FAIL %s:%d: %s\n", __FILE__, __LINE__, #cond); \
      ++g_failures;                                                        \
    }                                                                      \
  } while (0)

bool nearly_equal(double a, double b, double eps = 1e-6) {
  return std::fabs(a - b) <= eps;
}

void test_jitter_deterministic() {
  avsync::JitterSource a(12, 7);
  avsync::JitterSource b(12, 7);
  for (int i = 0; i < 100; ++i) {
    const int64_t x = a.next_delay_us();
    const int64_t y = b.next_delay_us();
    CHECK(x == y);
    CHECK(x >= 0 && x < 12 * 1000);
  }
  avsync::JitterSource zero(0, 1);
  CHECK(zero.next_delay_us() == 0);
}

void test_bounded_queue_backpressure_and_drain() {
  avsync::BoundedQueue<int> q(/*items*/ 2, /*bytes*/ 1000);
  int one = 1, two = 2, three = 3;
  CHECK(q.push(one, 4));
  CHECK(q.push(two, 4));
  CHECK(q.size() == 2);
  CHECK(!q.try_push(three, 4));  // Item cap reached, non-blocking rejection.

  auto got = q.try_pop();
  CHECK(got.has_value() && *got == 1);
  CHECK(q.try_push(three, 4));  // Room again.

  q.close();
  CHECK(q.closed());
  CHECK(q.drained() == false);  // Still holds items.
  auto v2 = q.pop();
  CHECK(v2.has_value() && *v2 == 2);
  auto v3 = q.pop();
  CHECK(v3.has_value() && *v3 == 3);
  CHECK(!q.pop().has_value());  // Drained after close.
  CHECK(q.drained());
}

void test_bounded_queue_error_releases_waiters() {
  avsync::BoundedQueue<int> q(1, 1000);
  int seven = 7, eight = 8;
  q.push(seven, 4);
  std::atomic<bool> pushed{false};
  std::thread producer([&] {
    pushed = q.push(eight, 4);  // Blocks until set_error/close wakes it.
  });
  std::this_thread::sleep_for(std::chrono::milliseconds(20));
  CHECK(!pushed.load());
  q.set_error();
  producer.join();
  CHECK(!pushed.load());  // push returned false due to error.
  CHECK(q.error());
}

void test_timestamp_normalizer_origin_and_fallback() {
  // time base 1/1000; first timestamp is non-zero (simulated start time).
  avsync::TimestampNormalizer norm(av_make_q(1, 1000));
  auto t0 = norm.normalize(/*pts*/ 5000, AV_NOPTS_VALUE, 0.033);
  CHECK(t0.has_value() && nearly_equal(*t0, 0.0));

  auto t1 = norm.normalize(5033, AV_NOPTS_VALUE, 0.033);
  CHECK(t1.has_value() && nearly_equal(*t1, 0.033));

  // Missing PTS falls back to DTS plus one frame duration.
  auto t2 = norm.normalize(AV_NOPTS_VALUE, /*dts*/ 5066, 0.033);
  CHECK(t2.has_value() && nearly_equal(*t2, 0.066 + 0.033, 1e-6));
}

void test_timestamp_regression_rebuilds_baseline() {
  avsync::TimestampNormalizer norm(av_make_q(1, 1000));
  norm.normalize(1000, AV_NOPTS_VALUE, 0.01);
  norm.normalize(1010, AV_NOPTS_VALUE, 0.01);
  // A timestamp earlier than the origin triggers baseline rebuild.
  auto after = norm.normalize(500, AV_NOPTS_VALUE, 0.01);
  CHECK(after.has_value() && *after >= 0.0);
}

void test_synchronizer_decisions() {
  avsync::SyncPolicy policy;
  policy.frame_duration_seconds = 1.0 / 30.0;
  avsync::Synchronizer sync(policy);
  double wait = 0.0;

  // Frame well in the future => wait.
  CHECK(sync.decide(0.500, 0.000, &wait) == avsync::SyncAction::Wait);
  CHECK(wait > 0.0);

  // Within the early threshold => show.
  CHECK(sync.decide(0.003, 0.000, &wait) == avsync::SyncAction::Show);

  // Slightly late but inside the drop timeout => show.
  CHECK(sync.decide(0.050, 0.100, &wait) == avsync::SyncAction::Show);

  // Far past due => drop.
  CHECK(sync.decide(0.000, 0.500, &wait) == avsync::SyncAction::Drop);
}

void test_synchronizer_repeat_gap() {
  avsync::SyncPolicy policy;
  policy.frame_duration_seconds = 1.0 / 30.0;  // ~33.3 ms
  avsync::Synchronizer sync(policy);

  // No frame shown yet: never repeat.
  CHECK(!sync.should_repeat(1.0, std::nullopt));

  const double last_pts = 0.100;
  // Clock barely past the last shown point: no repeat.
  CHECK(!sync.should_repeat(0.101, last_pts));
  // A full frame slot plus the gap threshold has elapsed: repeat.
  CHECK(sync.should_repeat(0.100 + policy.frame_duration_seconds + 0.010,
                           last_pts));
}

void test_master_clock_audio_driven() {
  avsync::MasterClock clock(48000);
  clock.start(1.0);
  // Consume 48000 samples => exactly one media second.
  clock.consume_audio(48000);
  CHECK(nearly_equal(clock.now_seconds(), 1.0, 1e-9));
  clock.consume_audio(24000);
  CHECK(nearly_equal(clock.now_seconds(), 1.5, 1e-9));
  CHECK(clock.has_audio());
}

void test_master_clock_wall_fallback() {
  // No audio track: monotonic clock drives the timeline directly.
  avsync::MasterClock clock(0);
  CHECK(!clock.has_audio());
  clock.start(1.0);
  std::this_thread::sleep_for(std::chrono::milliseconds(30));
  CHECK(clock.now_seconds() >= 0.025);
}

void test_master_clock_drift_is_clamped() {
  avsync::MasterClock clock(48000);
  clock.start(1.0);
  // Force a large residual by consuming far more than wall time allows.
  clock.consume_audio(48000 * 10);
  CHECK(std::fabs(clock.drift_ms()) <= 50.0 + 1e-9);
}

}  // namespace

int main() {
  test_jitter_deterministic();
  test_bounded_queue_backpressure_and_drain();
  test_bounded_queue_error_releases_waiters();
  test_timestamp_normalizer_origin_and_fallback();
  test_timestamp_regression_rebuilds_baseline();
  test_synchronizer_decisions();
  test_synchronizer_repeat_gap();
  test_master_clock_audio_driven();
  test_master_clock_wall_fallback();
  test_master_clock_drift_is_clamped();

  if (g_failures == 0) {
    std::printf("kernel_test: all tests passed\n");
    return 0;
  }
  std::printf("kernel_test: %d check(s) failed\n", g_failures);
  return 1;
}
