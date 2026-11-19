#pragma once

#include <atomic>
#include <cstdint>
#include <string>
#include <vector>

#include "bounded_queue.hpp"

namespace avsync {

struct SampleConfig {
    double duration_s = 12.0;
    int fps_num = 30000;
    int fps_den = 1001;
    int sample_rate = 48000;
    uint32_t seed = 42;
    // Non-zero stream start offset, exercised end to end.
    int64_t start_offset_us = 1000000;
    std::string path;  // output sample file (matroska)
};

// Generate a self-contained synthetic sample: mpeg4 video (with B-frames)
// + pcm_s16le stereo audio in a matroska container. Deterministic per seed.
bool generate_sample(const SampleConfig& cfg, std::string& err);

struct AudioChunk {
    int64_t pts_us = 0;  // media timeline, microseconds
    bool pts_valid = false;
    int sample_rate = 0;
    int channels = 0;
    int nb_samples = 0;  // per channel
    std::vector<int16_t> interleaved;
};

struct VideoFrameMsg {
    int64_t pts_us = 0;
    bool pts_valid = false;
    int64_t index = 0;  // presentation-order index
    int width = 0;
    int height = 0;
};

struct InjectionConfig {
    int video_delay_ms = 0;    // decode-latency spike injected every 7th frame
    int duplicate_every = 0;   // push every Nth decoded video frame twice
    int missing_pts_every = 0; // blank pts of every Nth decoded video frame
    int audio_underrun_ms = 0; // single audio stall of this duration at 40%
};

struct DecodeResult {
    bool ok = false;
    std::string error;
    int64_t video_frames = 0;
    int64_t audio_samples = 0;
    int64_t injected_duplicates = 0;
    int64_t injected_missing_pts = 0;
    int64_t start_time_us = 0;
};

// Blocking demux+decode of the sample file; pushes into the queues until
// EOF or until a queue is closed by the consumer. Injection of duplicates,
// missing pts and decode-latency spikes happens here. The optional stop flag
// lets an external abort unblock a push into a queue whose consumer died.
DecodeResult decode_into_queues(const std::string& path,
                                const InjectionConfig& inj,
                                BoundedQueue<AudioChunk>& audio_q,
                                BoundedQueue<VideoFrameMsg>& video_q,
                                std::atomic<bool>* stop = nullptr);

}  // namespace avsync
