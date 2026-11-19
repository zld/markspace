#include "audio_resample.hpp"

#include <cstddef>

struct AVCodecContext;
struct AVFrame;

namespace pwsync {

struct AudioConverter::Impl {
    int sample_rate = 0;
    int channels = 0;
    int frame_samples = 0;
};

AudioConverter::AudioConverter() : impl_(new Impl) {}

AudioConverter::~AudioConverter() {
    close();
    delete impl_;
}

bool AudioConverter::open(AVCodecContext* /*decoder*/, int target_sample_rate,
                          int target_channels, int target_samples_per_frame) {
    // Temporary stub: real libswresample conversion lands in the next batch.
    impl_->sample_rate = target_sample_rate;
    impl_->channels = target_channels;
    impl_->frame_samples = target_samples_per_frame;
    return true;
}

void AudioConverter::close() {}

bool AudioConverter::push(const AVFrame* /*frame*/, double /*pts_seconds*/,
                          const EmitFn& /*emit*/) {
    return true;
}

bool AudioConverter::flush(double /*next_pts_seconds*/, const EmitFn& /*emit*/) {
    return true;
}

int AudioConverter::sampleRate() const { return impl_->sample_rate; }
int AudioConverter::channels() const { return impl_->channels; }
int AudioConverter::frameSamples() const { return impl_->frame_samples; }

}  // namespace pwsync
