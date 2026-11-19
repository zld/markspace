#include "decode_pipeline.hpp"

#include <deque>
#include <cmath>
#include <memory>
#include <string>
#include <utility>
#include <vector>

extern "C" {
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/avutil.h>
#include <libavutil/error.h>
#include <libavutil/rational.h>
}

#include "audio_resample.hpp"

namespace pwsync {

namespace {

std::string avError(int code) {
    char buf[AV_ERROR_MAX_STRING_SIZE] = {0};
    av_strerror(code, buf, sizeof(buf));
    return std::string(buf);
}

StreamTimeBase toTimeBase(AVRational r) {
    return StreamTimeBase{r.num, r.den};
}

double avSeconds(std::int64_t ticks, AVRational tb) {
    return static_cast<double>(ticks) * av_q2d(tb);
}

struct StreamContext {
    int index = -1;
    MediaType type = MediaType::Other;
    AVCodecContext* codec = nullptr;
    std::unique_ptr<TimestampNormalizer> normalizer;
    double fallback_duration = 0.0;
    std::uint64_t packets = 0;
    std::uint64_t frames = 0;
    // Packet-level flags queued in decode order; one is consumed per emitted
    // frame. Frames buffered by decoder delay simply take later entries.
    std::deque<std::pair<bool, bool>> pending_flags;  // {repaired, discontinuity}
};

// Drains frames already buffered inside one decoder. flushed=true marks the
// end-of-stream drain so the simulator can count delay-output frames.
bool drainDecoder(StreamContext& sc, bool flushed, DecodeResult& result,
                  AudioConverter* converter, const DecodeConfig& config) {
    while (true) {
        AVFrame* frame = av_frame_alloc();
        if (!frame) {
            result.errors.push_back(
                {ErrorCategory::DecodeError, sc.index, "av_frame_alloc failed"});
            return false;
        }
        int rc = avcodec_receive_frame(sc.codec, frame);
        if (rc == AVERROR(EAGAIN) || rc == AVERROR_EOF) {
            av_frame_free(&frame);
            break;
        }
        if (rc < 0) {
            result.errors.push_back({ErrorCategory::DecodeError, sc.index,
                                     "avcodec_receive_frame: " + avError(rc)});
            ++result.decode_failures;
            av_frame_free(&frame);
            continue;
        }
        ++sc.frames;
        if (flushed) {
            ++result.flushed_frames;
        }
        bool queued_repair = false;
        bool queued_discont = false;
        if (!sc.pending_flags.empty()) {
            queued_repair = sc.pending_flags.front().first;
            queued_discont = sc.pending_flags.front().second;
            sc.pending_flags.pop_front();
        }
        const bool repaired = (frame->pts == AV_NOPTS_VALUE) || queued_repair;
        const double frame_pts =
            frame->pts != AV_NOPTS_VALUE
                ? avSeconds(frame->pts, sc.codec->pkt_timebase)
                : sc.normalizer->lastPtsSeconds();
        const double frame_dur =
            frame->duration > 0
                ? avSeconds(frame->duration, sc.codec->pkt_timebase)
                : sc.normalizer->lastDurationSeconds();

        NormalizedFrame nf;
        nf.type = sc.type;
        nf.stream_index = sc.index;
        nf.pts_repaired = repaired;
        nf.discontinuity = queued_discont;
        nf.pts_seconds = frame_pts;
        nf.duration_seconds = frame_dur;
        if (sc.type == MediaType::Video) {
            nf.width = frame->width;
            nf.height = frame->height;
            nf.keyframe = (frame->flags & AV_FRAME_FLAG_KEY) != 0;
            if (config.on_frame) {
                config.on_frame(nf, flushed);
            }
        } else if (converter) {
            converter->push(frame, frame_pts, [&](const NormalizedFrame& out) {
                if (config.on_frame) {
                    config.on_frame(out, flushed);
                }
            });
        }
        av_frame_free(&frame);
    }
    return true;
}

}  // namespace

DecodeResult runDecodePipeline(const std::string& path, const DecodeConfig& config) {
    DecodeResult result;
    AVFormatContext* fmt = nullptr;
    AudioConverter converter;
    StreamContext video;
    StreamContext audio;

    int rc = avformat_open_input(&fmt, path.c_str(), nullptr, nullptr);
    if (rc < 0) {
        result.errors.push_back(
            {ErrorCategory::OpenFailed, -1, "cannot open '" + path + "': " + avError(rc)});
        return result;
    }
    rc = avformat_find_stream_info(fmt, nullptr);
    if (rc < 0) {
        result.errors.push_back(
            {ErrorCategory::OpenFailed, -1, "stream probe failed: " + avError(rc)});
        avformat_close_input(&fmt);
        return result;
    }

    result.duration_seconds =
        fmt->duration > 0 ? static_cast<double>(fmt->duration) / AV_TIME_BASE : 0.0;
    if (result.duration_seconds > config.limits.max_duration_seconds) {
        result.errors.push_back(
            {ErrorCategory::LimitExceeded, -1,
             "media duration " + std::to_string(result.duration_seconds) +
                 "s exceeds limit " +
                 std::to_string(config.limits.max_duration_seconds) + "s"});
        avformat_close_input(&fmt);
        return result;
    }

    auto open_stream = [&](MediaType type, StreamContext& sc) -> bool {
        const AVMediaType ffmpeg_type =
            (type == MediaType::Video) ? AVMEDIA_TYPE_VIDEO : AVMEDIA_TYPE_AUDIO;
        int idx = av_find_best_stream(fmt, ffmpeg_type, -1, -1, nullptr, 0);
        if (idx < 0) {
            result.errors.push_back(
                {ErrorCategory::MissingStream, -1,
                 std::string("no ") + toString(type) + " stream found"});
            return false;
        }
        sc.index = idx;
        sc.type = type;
        const AVCodec* decoder = avcodec_find_decoder(fmt->streams[idx]->codecpar->codec_id);
        if (!decoder) {
            result.errors.push_back(
                {ErrorCategory::MissingStream, idx,
                 std::string("no decoder for ") + toString(type) + " stream"});
            sc.index = -1;
            return false;
        }
        sc.codec = avcodec_alloc_context3(decoder);
        if (!sc.codec || avcodec_parameters_to_context(sc.codec, fmt->streams[idx]->codecpar) < 0) {
            result.errors.push_back(
                {ErrorCategory::DecodeError, idx, "decoder context setup failed"});
            return false;
        }
        sc.codec->pkt_timebase = fmt->streams[idx]->time_base;
        if (avcodec_open2(sc.codec, decoder, nullptr) < 0) {
            result.errors.push_back(
                {ErrorCategory::DecodeError, idx,
                 std::string("cannot open ") + toString(type) + " decoder"});
            return false;
        }
        if (type == MediaType::Video) {
            AVRational fr = fmt->streams[idx]->avg_frame_rate;
            result.video_frame_rate = (fr.den > 0) ? av_q2d(fr) : 0.0;
            sc.fallback_duration =
                result.video_frame_rate > 0.0 ? 1.0 / result.video_frame_rate : 0.04;
            if (sc.codec->width > config.limits.max_frame_width ||
                sc.codec->height > config.limits.max_frame_height) {
                result.errors.push_back(
                    {ErrorCategory::LimitExceeded, idx,
                     "frame size exceeds configured limit"});
                return false;
            }
        } else {
            sc.fallback_duration = sc.codec->sample_rate > 0
                                       ? static_cast<double>(sc.codec->frame_size) /
                                             sc.codec->sample_rate
                                       : 0.02322;
            if (!converter.open(sc.codec, 48000, 2, 1024)) {
                result.errors.push_back(
                    {ErrorCategory::DecodeError, idx, "audio converter setup failed"});
                return false;
            }
            result.audio_sample_rate = converter.sampleRate();
            result.audio_channels = converter.channels();
            result.audio_frame_samples = converter.frameSamples();
        }
        sc.normalizer = std::make_unique<TimestampNormalizer>(
            config.discontinuity_tolerance_seconds, sc.fallback_duration);
        return true;
    };

    const bool have_video = open_stream(MediaType::Video, video);
    const bool have_audio = open_stream(MediaType::Audio, audio);
    if (!have_video || !have_audio) {
        converter.close();
        if (video.codec) {
            avcodec_free_context(&video.codec);
        }
        if (audio.codec) {
            avcodec_free_context(&audio.codec);
        }
        avformat_close_input(&fmt);
        return result;  // MissingStream/DecodeError already categorized
    }
    result.video_stream_index = video.index;
    result.audio_stream_index = audio.index;

    std::uint64_t decoded_total = 0;
    AVPacket* packet = av_packet_alloc();
    while (av_read_frame(fmt, packet) >= 0) {
        StreamContext* sc = nullptr;
        if (packet->stream_index == video.index) {
            sc = &video;
        } else if (packet->stream_index == audio.index) {
            sc = &audio;
        }
        if (sc) {
            ++sc->packets;
            const bool corrupt = (packet->flags & AV_PKT_FLAG_CORRUPT) != 0;
            auto norm = sc->normalizer->normalize(
                packet->pts, packet->pts != AV_NOPTS_VALUE, packet->duration,
                toTimeBase(fmt->streams[sc->index]->time_base));
            sc->pending_flags.emplace_back(norm.repaired, norm.discontinuity);
            if (config.on_packet) {
                NormalizedPacket np;
                np.type = sc->type;
                np.stream_index = sc->index;
                np.keyframe = (packet->flags & AV_PKT_FLAG_KEY) != 0;
                np.corrupt = corrupt;
                np.pts_repaired = norm.repaired;
                np.discontinuity = norm.discontinuity;
                np.pts_seconds = norm.pts_seconds;
                np.duration_seconds = norm.duration_seconds;
                np.original_time_base = toTimeBase(fmt->streams[sc->index]->time_base);
                np.original_pts = packet->pts;
                np.original_pts_valid = packet->pts != AV_NOPTS_VALUE;
                config.on_packet(np);
            }
            if (corrupt) {
                result.errors.push_back(
                    {ErrorCategory::CorruptPacket, sc->index,
                     "corrupt packet at pts=" + std::to_string(norm.pts_seconds)});
            }
            rc = avcodec_send_packet(sc->codec, packet);
            if (rc < 0 && rc != AVERROR(EAGAIN)) {
                result.errors.push_back(
                    {ErrorCategory::DecodeError, sc->index,
                     "avcodec_send_packet: " + avError(rc)});
            } else if (!drainDecoder(*sc, false, result,
                                     sc->type == MediaType::Audio ? &converter : nullptr,
                                     config)) {
                break;
            }
            decoded_total = video.frames + audio.frames;
            if (decoded_total > config.limits.max_decoded_frames) {
                result.errors.push_back(
                    {ErrorCategory::LimitExceeded, sc->index,
                     "decoded frame count exceeds limit"});
                av_packet_unref(packet);
                break;
            }
        }
        av_packet_unref(packet);
    }
    av_packet_free(&packet);

    // End-of-stream: flush both decoders and drain delayed frames.
    avcodec_send_packet(video.codec, nullptr);
    avcodec_send_packet(audio.codec, nullptr);
    drainDecoder(video, true, result, nullptr, config);
    drainDecoder(audio, true, result, &converter, config);
    converter.flush(audio.normalizer->lastPtsSeconds(),
                    [&](const NormalizedFrame& nf) {
                        ++result.flushed_frames;
                        ++audio.frames;
                        if (config.on_frame) {
                            config.on_frame(nf, true);
                        }
                    });

    result.timestamp_repairs += video.normalizer->repairs() + audio.normalizer->repairs();
    result.discontinuities += video.normalizer->discontinuities() +
                              audio.normalizer->discontinuities();
    result.ok = result.errors.empty();
    avcodec_free_context(&video.codec);
    avcodec_free_context(&audio.codec);
    converter.close();
    avformat_close_input(&fmt);
    return result;
}

}  // namespace pwsync
