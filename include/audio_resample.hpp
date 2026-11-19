#pragma once

#include <functional>

struct AVCodecContext;
struct AVFrame;

namespace pwsync {

struct NormalizedFrame;

// Converts decoded audio frames to packed s16 at a fixed common sample rate
// (48 kHz, stereo) and emits one NormalizedFrame per fixed-size output block,
// preserving PTS continuity. The only libswresample consumer.
class AudioConverter {
public:
    using EmitFn = std::function<void(const NormalizedFrame&)>;

    AudioConverter();
    ~AudioConverter();

    AudioConverter(const AudioConverter&) = delete;
    AudioConverter& operator=(const AudioConverter&) = delete;

    // target_samples_per_frame: output block size, e.g. 1024.
    bool open(AVCodecContext* decoder, int target_sample_rate,
              int target_channels, int target_samples_per_frame);
    void close();

    // Pushes one decoded frame and invokes emit for every complete output
    // block. pts_seconds is the presentation time of the first input sample;
    // subsequent blocks advance by block duration.
    bool push(const AVFrame* frame, double pts_seconds, const EmitFn& emit);

    // Drains remaining buffered samples as a final (possibly short) block.
    bool flush(double next_pts_seconds, const EmitFn& emit);

    int sampleRate() const;
    int channels() const;
    int frameSamples() const;

private:
    struct Impl;
    Impl* impl_;
};

}  // namespace pwsync
