#pragma once

#include <condition_variable>
#include <cstddef>
#include <deque>
#include <mutex>
#include <utility>

namespace avsync {

// Bounded blocking MPMC queue. close() wakes every waiter; after close,
// push() fails and pop() drains remaining items before returning false.
template <typename T>
class BoundedQueue {
public:
    explicit BoundedQueue(size_t capacity) : capacity_(capacity) {}
    BoundedQueue(const BoundedQueue&) = delete;
    BoundedQueue& operator=(const BoundedQueue&) = delete;

    // Blocks while full; returns false if the queue is closed.
    bool push(T item) {
        std::unique_lock<std::mutex> lk(mu_);
        not_full_.wait(lk, [&] { return closed_ || items_.size() < capacity_; });
        if (closed_) return false;
        items_.push_back(std::move(item));
        lk.unlock();
        not_empty_.notify_one();
        return true;
    }

    // Blocks while empty; returns false once closed and drained.
    bool pop(T& out) {
        std::unique_lock<std::mutex> lk(mu_);
        not_empty_.wait(lk, [&] { return closed_ || !items_.empty(); });
        if (items_.empty()) return false;
        out = std::move(items_.front());
        items_.pop_front();
        lk.unlock();
        not_full_.notify_one();
        return true;
    }

    // Pop with a timeout. Sets timed_out when the wait expired.
    template <typename Rep, typename Period>
    bool pop_for(T& out, const std::chrono::duration<Rep, Period>& d,
                 bool& timed_out) {
        std::unique_lock<std::mutex> lk(mu_);
        timed_out = false;
        if (!not_empty_.wait_for(lk, d,
                                 [&] { return closed_ || !items_.empty(); })) {
            timed_out = true;
            return false;
        }
        if (items_.empty()) return false;
        out = std::move(items_.front());
        items_.pop_front();
        lk.unlock();
        not_full_.notify_one();
        return true;
    }

    void close() {
        std::lock_guard<std::mutex> lk(mu_);
        closed_ = true;
        not_full_.notify_all();
        not_empty_.notify_all();
    }

    bool closed() const {
        std::lock_guard<std::mutex> lk(mu_);
        return closed_;
    }

    size_t size() const {
        std::lock_guard<std::mutex> lk(mu_);
        return items_.size();
    }

private:
    const size_t capacity_;
    mutable std::mutex mu_;
    std::condition_variable not_full_;
    std::condition_variable not_empty_;
    std::deque<T> items_;
    bool closed_ = false;
};

}  // namespace avsync
