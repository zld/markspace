#pragma once

#include <atomic>
#include <chrono>
#include <condition_variable>
#include <deque>
#include <mutex>
#include <optional>

#include "avsync/common.hpp"
#include "avsync/queue.hpp"

namespace avsync {

// A decoded frame plus its position on the normalized zero-based timeline.
struct DecodedFrame {
  FramePtr frame;
  double pts_seconds = 0.0;
  double duration_seconds = 0.0;
};

// Frame queue with a presentation-time water mark. A decoder may only run a
// bounded lead (in media seconds) ahead of the playback clock; once the queued
// frames reach that lead, push waits. This throttles the whole producer chain
// (demuxer + decoders) to real time so queues neither exhaust memory nor reach
// end-of-stream long before playback has consumed their contents.
class WatermarkFrameQueue {
 public:
  WatermarkFrameQueue(size_t max_items, size_t max_bytes, double lead_seconds)
      : max_items_(max_items),
        max_bytes_(max_bytes),
        lead_seconds_(lead_seconds) {}

  // Set the current playback position (media seconds). Called by the scheduler.
  void set_clock_seconds(double seconds) {
    std::lock_guard<std::mutex> lock(mutex_);
    clock_seconds_ = seconds;
    not_full_.notify_all();
  }

  // Block until the frame fits the item/byte caps AND its PTS is within the
  // lead window. Returns false on close/error/stop.
  bool push(DecodedFrame item, size_t item_bytes,
            const std::atomic<bool>& stop) {
    std::unique_lock<std::mutex> lock(mutex_);
    not_full_.wait(lock, [&] {
      if (closed_ || error_ || stop.load()) return true;
      const bool room = items_.size() < max_items_ &&
                        bytes_ + item_bytes <= max_bytes_;
      const bool in_window =
          item.pts_seconds <= clock_seconds_ + lead_seconds_;
      return room && in_window;
    });
    if (closed_ || error_ || stop.load()) return false;
    bytes_ += item_bytes;
    items_.emplace_back(std::move(item), item_bytes);
    not_empty_.notify_one();
    return true;
  }

  std::optional<DecodedFrame> try_pop() {
    std::lock_guard<std::mutex> lock(mutex_);
    if (items_.empty()) return std::nullopt;
    return pop_locked();
  }

  template <typename Rep, typename Period>
  std::optional<DecodedFrame> pop_timeout(
      const std::chrono::duration<Rep, Period>& wait) {
    std::unique_lock<std::mutex> lock(mutex_);
    if (!not_empty_.wait_for(lock, wait,
                             [&] { return closed_ || error_ || !items_.empty(); })) {
      return std::nullopt;
    }
    if (items_.empty()) return std::nullopt;
    return pop_locked();
  }

  void close() {
    std::lock_guard<std::mutex> lock(mutex_);
    closed_ = true;
    not_empty_.notify_all();
    not_full_.notify_all();
  }

  void set_error() {
    std::lock_guard<std::mutex> lock(mutex_);
    error_ = true;
    not_empty_.notify_all();
    not_full_.notify_all();
  }

  bool error() const {
    std::lock_guard<std::mutex> lock(mutex_);
    return error_;
  }
  bool closed() const {
    std::lock_guard<std::mutex> lock(mutex_);
    return closed_;
  }
  size_t size() const {
    std::lock_guard<std::mutex> lock(mutex_);
    return items_.size();
  }
  bool drained() const {
    std::lock_guard<std::mutex> lock(mutex_);
    return (closed_ || error_) && items_.empty();
  }

 private:
  DecodedFrame pop_locked() {
    Entry entry = std::move(items_.front());
    items_.pop_front();
    bytes_ -= entry.bytes;
    not_full_.notify_all();
    return std::move(entry.item);
  }

  struct Entry {
    DecodedFrame item;
    size_t bytes;
    Entry(DecodedFrame it, size_t b) : item(std::move(it)), bytes(b) {}
  };

  mutable std::mutex mutex_;
  std::condition_variable not_full_;
  std::condition_variable not_empty_;
  std::deque<Entry> items_;
  size_t max_items_;
  size_t max_bytes_;
  double lead_seconds_;
  double clock_seconds_ = 0.0;
  size_t bytes_ = 0;
  bool closed_ = false;
  bool error_ = false;
};

}  // namespace avsync
