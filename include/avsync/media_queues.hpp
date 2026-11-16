#pragma once

#include "avsync/common.hpp"
#include "avsync/queue.hpp"
#include "avsync/watermark_queue.hpp"

namespace avsync {

// Compressed packets routed from the demuxer to each decoder.
using PacketQueue = BoundedQueue<PacketPtr>;

// DecodedFrame is defined in watermark_queue.hpp. The PTS water mark keeps
// decoders a bounded lead ahead of the playback clock.
using FrameQueue = WatermarkFrameQueue;

}  // namespace avsync
