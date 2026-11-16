#pragma once

#include <cstddef>

namespace avsync {

// Total memory ceiling for every bounded queue combined: 256 MiB.
constexpr size_t kTotalQueueBudgetBytes = 256ULL * 1024 * 1024;

// Per-queue caps; their sum stays well below the total budget.
constexpr size_t kPacketQueueBytes = 64ULL * 1024 * 1024;
constexpr size_t kVideoFrameQueueBytes = 160ULL * 1024 * 1024;
constexpr size_t kAudioFrameQueueBytes = 16ULL * 1024 * 1024;
constexpr size_t kPacketQueueItems = 1024;
constexpr size_t kVideoFrameQueueItems = 128;
constexpr size_t kAudioFrameQueueItems = 256;

}  // namespace avsync
