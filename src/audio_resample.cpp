#include "audio_resample.hpp"

#include <deque>
#include <cstring>
#include <new>

extern "C" {
#include <libavcodec/avcodec.h>
#include <libavutil/audio_fifo.h>
#include <libavutil/channel_layout.h>
#include <libavutil/error.h>
#include <libavutil/frame.h>
#include <libavutil/mathematics.h>
#include <libavutil/mem.h>
#include <libavutil/samplefmt.h>
#include <libswresample/swresample.h>
}

#include "media_types.hpp"

namespace pwsync {

namespace {
constexpr AVSampleFormat kTargetFormat = AV_SAMPLE_FMT_S16;
}  // namespace

struct AudioConverter::Impl {
    SwrContext* swr = nullptr;
    AVAudioFifo* fifo = nullptr;
    AVChannelLayout out_layout{};
    uint8_t** out_data = nullptr;  // one converted swr frame at a time
    int out_capacity = 0;
    uint8_t** block_data = nullptr;  // one fixed-size emitted block
    int block_linesize = 0;
    int sample_rate = 0;
    int channels = 0;
    int frame_samples = 0;
    bool opened = false;
    // Piecewise-linear PTS anchors: cumulative input sample position and the
    // PTS reported by the encoder for that position. Packet-level PTS jitter
    // lives between anchors and is preserved when output blocks are mapped.
    struct Anchor {
        std::int64_t input_position;
        double pts_seconds;
    };
    std::deque<Anchor> anchors;
    int input_sample_rate = 0;
    std::int64_t total_input_samples = 0;
    std::int64_t total_emitted_samples = 0;
};

AudioConverter::AudioConverter() : impl_(new Impl) {}

AudioConverter::~AudioConverter() {
    close();
    delete impl_;
}

bool AudioConverter::open(AVCodecContext* decoder, int target_sample_rate,
                          int target_channels, int target_samples_per_frame) {
    close();
    impl_->sample_rate = target_sample_rate;
    impl_->channels = target_channels;
    impl_->frame_samples = target_samples_per_frame;

    av_channel_layout_default(&impl_->out_layout, target_channels);
    int rc = swr_alloc_set_opts2(
        &impl_->swr, &impl_->out_layout, kTargetFormat, target_sample_rate,
        &decoder->ch_layout, static_cast<AVSampleFormat>(decoder->sample_fmt),
        decoder->sample_rate, 0, nullptr);
    if (rc < 0 || swr_init(impl_->swr) < 0) {
        close();
        return false;
    }
    impl_->fifo = av_audio_fifo_alloc(kTargetFormat, target_channels, target_samples_per_frame);
    if (!impl_->fifo) {
        close();
        return false;
    }
    if (av_samples_alloc_array_and_samples(&impl_->block_data, &impl_->block_linesize,
                                           target_channels, target_samples_per_frame,
                                           kTargetFormat, 0) < 0) {
        close();
        return false;
    }
    impl_->out_capacity = 0;
    impl_->opened = true;
    impl_->input_sample_rate = decoder->sample_rate;
    return true;
}

void AudioConverter::close() {
    if (impl_->out_data) {
        if (av_sample_fmt_is_planar(kTargetFormat)) {
            av_freep(&impl_->out_data[0]);
        }
        av_freep(&impl_->out_data);
    }
    if (impl_->block_data) {
        av_freep(&impl_->block_data[0]);
        av_freep(&impl_->block_data);
    }
    if (impl_->fifo) {
        av_audio_fifo_free(impl_->fifo);
    }
    if (impl_->swr) {
        swr_free(&impl_->swr);
    }
    av_channel_layout_uninit(&impl_->out_layout);
    impl_->fifo = nullptr;
    impl_->swr = nullptr;
    impl_->out_data = nullptr;
    impl_->block_data = nullptr;
    impl_->opened = false;
    impl_->anchors.clear();
    impl_->input_sample_rate = 0;
    impl_->total_input_samples = 0;
    impl_->total_emitted_samples = 0;
}

int AudioConverter::sampleRate() const { return impl_->sample_rate; }
int AudioConverter::channels() const { return impl_->channels; }
int AudioConverter::frameSamples() const { return impl_->frame_samples; }

bool AudioConverter::push(const AVFrame* frame, double pts_seconds, const EmitFn& emit) {
    if (!impl_->opened) {
        return false;
    }
    // Anchor at the start of this input frame before consuming its samples.
    impl_->anchors.push_back({impl_->total_input_samples, pts_seconds});

    const std::int64_t delay = swr_get_delay(impl_->swr, frame->sample_rate);
    int expected = static_cast<int>(av_rescale_rnd(
        delay + frame->nb_samples, impl_->sample_rate, frame->sample_rate, AV_ROUND_UP));
    if (expected > impl_->out_capacity) {
        if (impl_->out_data) {
            av_freep(&impl_->out_data[0]);
            av_freep(&impl_->out_data);
        }
        impl_->out_data = nullptr;
        impl_->out_capacity = 0;
        if (av_samples_alloc_array_and_samples(
                &impl_->out_data, nullptr, impl_->channels, expected, kTargetFormat, 0) < 0) {
            return false;
        }
        impl_->out_capacity = expected;
    }
    int converted = swr_convert(impl_->swr, impl_->out_data, expected,
                                const_cast<const uint8_t**>(frame->extended_data),
                                frame->nb_samples);
    if (converted < 0) {
        return false;
    }
    impl_->total_input_samples += frame->nb_samples;
    if (av_audio_fifo_realloc(impl_->fifo, av_audio_fifo_size(impl_->fifo) + converted) < 0) {
        return false;
    }
    if (av_audio_fifo_write(impl_->fifo, reinterpret_cast<void* const*>(impl_->out_data),
                            converted) < 0) {
        return false;
    }

    // Map an output-sample offset back to an input-sample position, then
    // interpolate within the surrounding encoder PTS anchors.
    auto map_pts = [&](std::int64_t emitted_offset) -> double {
        const std::int64_t input_pos = av_rescale_q(
            emitted_offset, AVRational{1, impl_->sample_rate},
            AVRational{1, impl_->input_sample_rate});
        while (impl_->anchors.size() >= 2 &&
               impl_->anchors[1].input_position <= input_pos) {
            impl_->anchors.pop_front();
        }
        const auto& a0 = impl_->anchors.front();
        if (impl_->anchors.size() == 1) {
            return a0.pts_seconds +
                   static_cast<double>(input_pos - a0.input_position) /
                       impl_->input_sample_rate;
        }
        const auto& a1 = impl_->anchors[1];
        const double span = static_cast<double>(a1.input_position - a0.input_position);
        const double frac = span > 0.0
                                ? static_cast<double>(input_pos - a0.input_position) / span
                                : 0.0;
        return a0.pts_seconds + frac * (a1.pts_seconds - a0.pts_seconds);
    };

    while (av_audio_fifo_size(impl_->fifo) >= impl_->frame_samples) {
        if (av_audio_fifo_read(impl_->fifo,
                               reinterpret_cast<void* const*>(impl_->block_data),
                               impl_->frame_samples) < impl_->frame_samples) {
            return false;
        }
        NormalizedFrame nf;
        nf.type = MediaType::Audio;
        nf.pts_seconds = map_pts(impl_->total_emitted_samples);
        nf.duration_seconds =
            static_cast<double>(impl_->frame_samples) / impl_->sample_rate;
        nf.sample_rate = impl_->sample_rate;
        nf.channels = impl_->channels;
        nf.nb_samples = impl_->frame_samples;
        emit(nf);
        impl_->total_emitted_samples += impl_->frame_samples;
    }
    return true;
}

bool AudioConverter::flush(double next_pts_seconds, const EmitFn& emit) {
    if (!impl_->opened) {
        return false;
    }
    // Drain the resampler's internal delay first.
    if (impl_->out_capacity < impl_->frame_samples) {
        if (impl_->out_data) {
            av_freep(&impl_->out_data[0]);
            av_freep(&impl_->out_data);
        }
        if (av_samples_alloc_array_and_samples(
                &impl_->out_data, nullptr, impl_->channels, impl_->frame_samples,
                kTargetFormat, 0) < 0) {
            return false;
        }
        impl_->out_capacity = impl_->frame_samples;
    }
    while (true) {
        int got = swr_convert(impl_->swr, impl_->out_data, impl_->out_capacity,
                              nullptr, 0);
        if (got <= 0) {
            break;
        }
        if (av_audio_fifo_realloc(impl_->fifo, av_audio_fifo_size(impl_->fifo) + got) < 0) {
            return false;
        }
        if (av_audio_fifo_write(impl_->fifo,
                                reinterpret_cast<void* const*>(impl_->out_data), got) < 0) {
            return false;
        }
    }
    int remaining = av_audio_fifo_size(impl_->fifo);
    if (remaining <= 0) {
        return true;
    }
    // Emit one final (possibly short) block; extrapolate from the last anchor.
    (void)next_pts_seconds;
    if (av_audio_fifo_read(impl_->fifo, reinterpret_cast<void* const*>(impl_->block_data),
                           remaining) < remaining) {
        return false;
    }
    const std::int64_t input_pos = av_rescale_q(
        impl_->total_emitted_samples, AVRational{1, impl_->sample_rate},
        AVRational{1, impl_->input_sample_rate});
    const auto& a0 = impl_->anchors.back();
    NormalizedFrame nf;
    nf.type = MediaType::Audio;
    nf.pts_seconds = a0.pts_seconds +
                     static_cast<double>(input_pos - a0.input_position) /
                         impl_->input_sample_rate;
    nf.duration_seconds = static_cast<double>(remaining) / impl_->sample_rate;
    nf.sample_rate = impl_->sample_rate;
    nf.channels = impl_->channels;
    nf.nb_samples = remaining;
    emit(nf);
    impl_->total_emitted_samples += remaining;
    return true;
}

}  // namespace pwsync
