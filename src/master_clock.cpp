#include "master_clock.hpp"

#include <chrono>

namespace avsync {

int64_t MasterClock::wall_us() {
    return std::chrono::duration_cast<std::chrono::microseconds>(
               std::chrono::steady_clock::now().time_since_epoch())
        .count();
}

MasterClock::MasterClock() : base_wall_us_(wall_us()), ideal_wall_us_(base_wall_us_) {}

void MasterClock::start(int64_t media_us) {
    std::lock_guard<std::mutex> lk(mu_);
    base_media_us_ = media_us;
    base_wall_us_ = wall_us();
    ideal_media_us_ = media_us;
    ideal_wall_us_ = base_wall_us_;
    running_ = true;
}

void MasterClock::pause() {
    std::lock_guard<std::mutex> lk(mu_);
    if (!running_) return;
    // Freeze at the current value.
    base_media_us_ = base_media_us_ + (wall_us() - base_wall_us_);
    base_wall_us_ = wall_us();
    running_ = false;
}

void MasterClock::resume() {
    std::lock_guard<std::mutex> lk(mu_);
    if (running_) return;
    base_wall_us_ = wall_us();
    running_ = true;
}

void MasterClock::reset(int64_t media_us) {
    std::lock_guard<std::mutex> lk(mu_);
    base_media_us_ = media_us;
    base_wall_us_ = wall_us();
}

void MasterClock::set(int64_t media_us) {
    std::lock_guard<std::mutex> lk(mu_);
    base_media_us_ = media_us;
    base_wall_us_ = wall_us();
}

int64_t MasterClock::now_us() const {
    std::lock_guard<std::mutex> lk(mu_);
    int64_t t = base_media_us_;
    if (running_) t += wall_us() - base_wall_us_;
    if (t < last_returned_us_) t = last_returned_us_;
    last_returned_us_ = t;
    return t;
}

bool MasterClock::running() const {
    std::lock_guard<std::mutex> lk(mu_);
    return running_;
}

int64_t MasterClock::drift_us() const {
    std::lock_guard<std::mutex> lk(mu_);
    int64_t t = base_media_us_;
    if (running_) t += wall_us() - base_wall_us_;
    if (t < last_returned_us_) t = last_returned_us_;
    int64_t ideal = ideal_media_us_ + (wall_us() - ideal_wall_us_);
    return t - ideal;
}

}  // namespace avsync
