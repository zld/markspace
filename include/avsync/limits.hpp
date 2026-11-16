#pragma once

#include <cstddef>

namespace avsync {

// Total memory ceiling for every bounded queue combined: 256 MiB.
constexpr size_t kTotalQueueBudgetBytes = 256ULL * 1024 * 1024;

// Per-queue caps. The worst-case sum of all four queues below stays under the
// 256 MiB total budget; steady-state occupancy is far lower because the PTS
// water mark limits decoded frames to kAudioLeadSeconds/kVideoLeadSeconds.
constexpr size_t kPacketQueueBytes = 32ULL * 1024 * 1024;
constexpr size_t kVideoFrameQueueBytes = 160ULL * 1024 * 1024;
constexpr size_t kAudioFrameQueueBytes = 16ULL * 1024 * 1024;
// 2*32 + 160 + 16 = 240 MiB, leaving headroom for the bounded audio PCM
// backlog and per-frame allocations within the 256 MiB ceiling.
constexpr size_t kPacketQueueItems = 64;
constexpr size_t kVideoFrameQueueItems = 32;
constexpr size_t kAudioFrameQueueItems = 128;

// How far (media seconds) a decoder may run ahead of the playback clock.
// Small enough to bound latency and memory, large enough to absorb jitter.
constexpr double kVideoLeadSeconds = 1.0;
constexpr double kAudioLeadSeconds = 1.0;

}  // namespace avsync
