#pragma once

#include "avsync/common.hpp"
#include "avsync/queue.hpp"

namespace avsync {

// Compressed packets routed from the demuxer to each decoder.
using PacketQueue = BoundedQueue<PacketPtr>;

// A decoded frame plus its position on the normalized zero-based timeline.
struct DecodedFrame {
  FramePtr frame;
  double pts_seconds = 0.0;
  double duration_seconds = 0.0;
};

// Decoded frames waiting for the audio/video output paths.
using FrameQueue = BoundedQueue<DecodedFrame>;

}  // namespace avsync
