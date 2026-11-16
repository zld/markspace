#pragma once

#include <chrono>
#include <condition_variable>
#include <cstddef>
#include <deque>
#include <mutex>
#include <optional>
#include <utility>

namespace avsync {

// Bounded MPSC-style queue with both item-count and byte-size caps, so decoder
// backpressure cannot grow memory without limit. Once closed (end-of-stream or
// an upstream error) waiting producers and consumers are released deterministically.
template <typename T>
class BoundedQueue {
 public:
  BoundedQueue(size_t max_items, size_t max_bytes)
      : max_items_(max_items), max_bytes_(max_bytes) {}

  BoundedQueue(const BoundedQueue&) = delete;
  BoundedQueue& operator=(const BoundedQueue&) = delete;

  // Block while full. Returns false when the queue was closed or an error was
  // raised; true when the item was accepted.
  bool push(T item, size_t item_bytes) {
    std::unique_lock<std::mutex> lock(mutex_);
    not_full_.wait(lock, [&] {
      return closed_ || error_ ||
             (items_.size() < max_items_ && bytes_ + item_bytes <= max_bytes_);
    });
    if (closed_ || error_) return false;
    bytes_ += item_bytes;
    items_.emplace_back(std::move(item), item_bytes);
    not_empty_.notify_one();
    return true;
  }

  // Non-blocking pop for scheduler loops that must keep ticking (frame repeat,
  // audio underrun handling).
  std::optional<T> try_pop() {
    std::lock_guard<std::mutex> lock(mutex_);
    if (items_.empty()) return std::nullopt;
    return pop_locked();
  }

  // Non-blocking push. Returns false (item left untouched) when the queue is
  // full, closed or errored; lets a demuxer service the other stream instead
  // of blocking one path behind the other's backpressure.
  bool try_push(T& item, size_t item_bytes) {
    std::lock_guard<std::mutex> lock(mutex_);
    if (closed_ || error_) return false;
    if (items_.size() >= max_items_ || bytes_ + item_bytes > max_bytes_) {
      return false;
    }
    bytes_ += item_bytes;
    items_.emplace_back(std::move(item), item_bytes);
    not_empty_.notify_one();
    return true;
  }

  // Blocking pop for worker threads. nullopt means drained + closed or error.
  std::optional<T> pop() {
    std::unique_lock<std::mutex> lock(mutex_);
    not_empty_.wait(lock, [&] { return closed_ || error_ || !items_.empty(); });
    if (items_.empty()) return std::nullopt;
    return pop_locked();
  }

  // Bounded blocking pop used by the audio path on underrun: waits for the
  // decoder instead of burning scheduler wall-time in a busy loop. nullopt on
  // timeout, drain or error.
  template <typename Rep, typename Period>
  std::optional<T> pop_timeout(const std::chrono::duration<Rep, Period>& wait) {
    std::unique_lock<std::mutex> lock(mutex_);
    if (!not_empty_.wait_for(lock, wait,
                             [&] { return closed_ || error_ || !items_.empty(); })) {
      return std::nullopt;
    }
    if (items_.empty()) return std::nullopt;
    return pop_locked();
  }

  // End-of-stream: queued items remain drainable, then pop() returns nullopt.
  void close() {
    std::lock_guard<std::mutex> lock(mutex_);
    closed_ = true;
    not_empty_.notify_all();
    not_full_.notify_all();
  }

  // Upstream failure: wake everyone; remaining items are abandoned.
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

  size_t byte_size() const {
    std::lock_guard<std::mutex> lock(mutex_);
    return bytes_;
  }

  // True once closed/errored and every queued item has been consumed.
  bool drained() const {
    std::lock_guard<std::mutex> lock(mutex_);
    return (closed_ || error_) && items_.empty();
  }

 private:
  T pop_locked() {
    Entry entry = std::move(items_.front());
    items_.pop_front();
    bytes_ -= entry.bytes;
    not_full_.notify_one();
    return std::move(entry.item);
  }

  struct Entry {
    T item;
    size_t bytes;
    Entry(T it, size_t b) : item(std::move(it)), bytes(b) {}
  };

  mutable std::mutex mutex_;
  std::condition_variable not_full_;
  std::condition_variable not_empty_;
  std::deque<Entry> items_;
  size_t max_items_;
  size_t max_bytes_;
  size_t bytes_ = 0;
  bool closed_ = false;
  bool error_ = false;
};

}  // namespace avsync
