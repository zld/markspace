#include "fixture_generator.hpp"

#include <cstdint>
#include <cstring>
#include <string>

extern "C" {
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/audio_fifo.h>
#include <libavutil/channel_layout.h>
#include <libavutil/frame.h>
#include <libavutil/imgutils.h>
#include <libavutil/mem.h>
#include <libavutil/opt.h>
#include <libavutil/rational.h>
}

namespace pwsync {

namespace {

std::string err(int rc) {
    char buf[AV_ERROR_MAX_STRING_SIZE] = {0};
    av_strerror(rc, buf, sizeof(buf));
    return buf;
}

// Deterministic LCG: same content on every machine and every run.
std::uint32_t nextLcg(std::uint32_t& state) {
    state = state * 1103515245u + 12345u;
    return state;
}

bool configureVideo(AVCodecContext* ctx, const FixtureOptions& o, AVRational tb) {
    ctx->width = o.width;
    ctx->height = o.height;
    ctx->pix_fmt = AV_PIX_FMT_YUV420P;
    ctx->time_base = tb;
    ctx->framerate = AVRational{o.frame_rate_num, o.frame_rate_den};
    ctx->max_b_frames = 0;  // no reordering: PTS == DTS, simpler diagnostics
    ctx->gop_size = 15;
    av_opt_set(ctx->priv_data, "threads", "1", 0);  // deterministic encoding
    return avcodec_open2(ctx, avcodec_find_encoder(ctx->codec_id), nullptr) >= 0;
}

bool configureAudio(AVCodecContext* ctx, const FixtureOptions& o) {
    ctx->sample_rate = o.sample_rate;
    ctx->sample_fmt = AV_SAMPLE_FMT_FLTP;  // native AAC encoder input
    av_channel_layout_default(&ctx->ch_layout, o.audio_channels);
    ctx->bit_rate = 64000;
    av_opt_set(ctx->priv_data, "threads", "1", 0);
    return avcodec_open2(ctx, avcodec_find_encoder(ctx->codec_id), nullptr) >= 0;
}

void paintFrame(AVFrame* frame, int index, std::uint32_t& rng) {
    const int chroma = index % 256;
    for (int y = 0; y < frame->height; ++y) {
        std::uint8_t* row = frame->data[0] + y * frame->linesize[0];
        for (int x = 0; x < frame->width; ++x) {
            row[x] = static_cast<std::uint8_t>((x + y + index) & 0xFF);
        }
    }
    for (int p = 1; p <= 2; ++p) {
        for (int y = 0; y < frame->height / 2; ++y) {
            std::uint8_t* row = frame->data[p] + y * frame->linesize[p];
            for (int x = 0; x < frame->width / 2; ++x) {
                row[x] = static_cast<std::uint8_t>((chroma + nextLcg(rng)) & 0xFF);
            }
        }
    }
}

bool encodeFrame(AVCodecContext* enc, AVFrame* frame, AVPacket* pkt,
                 AVFormatContext* fmt, AVStream* st) {
    if (avcodec_send_frame(enc, frame) < 0) {
        return false;
    }
    while (true) {
        int rc = avcodec_receive_packet(enc, pkt);
        if (rc == AVERROR(EAGAIN) || rc == AVERROR_EOF) {
            return true;
        }
        if (rc < 0) {
            return false;
        }
        av_packet_rescale_ts(pkt, enc->time_base, st->time_base);
        pkt->stream_index = st->index;
        if (av_interleaved_write_frame(fmt, pkt) < 0) {
            return false;
        }
        av_packet_unref(pkt);
    }
}

}  // namespace

bool generateFixture(const FixtureOptions& o, std::string& error_message) {
    AVFormatContext* fmt = nullptr;
    int rc = avformat_alloc_output_context2(&fmt, nullptr, "mp4", o.output_path.c_str());
    if (rc < 0 || !fmt) {
        error_message = "cannot allocate mp4 muxer: " + err(rc);
        return false;
    }
    const AVCodec* venc = avcodec_find_encoder(AV_CODEC_ID_MPEG4);
    const AVCodec* aenc = avcodec_find_encoder(AV_CODEC_ID_AAC);
    if (!venc || !aenc) {
        error_message = "mpeg4/aac encoder unavailable";
        avformat_free_context(fmt);
        return false;
    }
    AVStream* vst = avformat_new_stream(fmt, nullptr);
    AVStream* ast = avformat_new_stream(fmt, nullptr);
    AVCodecContext* vctx = avcodec_alloc_context3(venc);
    AVCodecContext* actx = avcodec_alloc_context3(aenc);
    const AVRational video_tb{1, 30000};  // mpeg4 caps the denominator at 65535
    if (!vst || !ast || !vctx || !actx ||
        !configureVideo(vctx, o, video_tb) || !configureAudio(actx, o)) {
        error_message = "encoder configuration failed";
        goto fail;
    }
    if (avcodec_parameters_from_context(vst->codecpar, vctx) < 0 ||
        avcodec_parameters_from_context(ast->codecpar, actx) < 0) {
        error_message = "cannot copy codec parameters";
        goto fail;
    }
    vst->time_base = vctx->time_base;
    ast->time_base = AVRational{1, actx->sample_rate};
    if (!(fmt->oformat->flags & AVFMT_NOFILE)) {
        if (avio_open(&fmt->pb, o.output_path.c_str(), AVIO_FLAG_WRITE) < 0) {
            error_message = "cannot open output file: " + o.output_path;
            goto fail;
        }
    }
    if (avformat_write_header(fmt, nullptr) < 0) {
        error_message = "write header failed";
        goto fail;
    }

    // ---- Video: VFR pattern + explicit hazard shifts --------------------
    {
        AVPacket* pkt = av_packet_alloc();
        AVFrame* frame = av_frame_alloc();
        frame->format = vctx->pix_fmt;
        frame->width = o.width;
        frame->height = o.height;
        av_frame_get_buffer(frame, 0);
        std::uint32_t rng = o.seed;
        double shift_seconds = 0.0;
        std::int64_t pts_ticks =
            static_cast<std::int64_t>(o.video_start_seconds * video_tb.den);
        const std::int64_t nominal_ticks =
            static_cast<std::int64_t>(video_tb.den *
                                      static_cast<double>(o.frame_rate_den) /
                                      o.frame_rate_num);
        for (int i = 0; i < o.video_frames; ++i) {
            const double nominal =
                static_cast<double>(i) * o.frame_rate_den / o.frame_rate_num;
            for (const auto& h : o.hazards) {
                if (std::abs(nominal - h.at_seconds) <
                    0.5 * o.frame_rate_den / o.frame_rate_num + 1e-9) {
                    shift_seconds += h.shift_seconds;
                }
            }
            // VFR: add this frame's extra tick to the running PTS so frame
            // durations genuinely vary rather than only differing on paper.
            const int extra = o.vfr_extra_ticks.empty()
                                  ? 0
                                  : o.vfr_extra_ticks[i % o.vfr_extra_ticks.size()];
            pts_ticks += extra;
            const std::int64_t shift_ticks =
                static_cast<std::int64_t>(shift_seconds * video_tb.den);
            frame->pts = pts_ticks + shift_ticks;
            // Duration reaches the next frame's PTS (or nominal period for
            // the final frame); hazards widen the gap automatically.
            const int next_extra =
                (i + 1 < o.video_frames && !o.vfr_extra_ticks.empty())
                    ? o.vfr_extra_ticks[(i + 1) % o.vfr_extra_ticks.size()]
                    : 0;
            frame->duration = nominal_ticks + next_extra;
            av_frame_make_writable(frame);
            paintFrame(frame, i, rng);
            if (!encodeFrame(vctx, frame, pkt, fmt, vst)) {
                error_message = "video encode failed at frame " + std::to_string(i);
                av_packet_free(&pkt);
                av_frame_free(&frame);
                goto fail;
            }
            pts_ticks += nominal_ticks;
        }
        if (!encodeFrame(vctx, nullptr, pkt, fmt, vst)) {
            error_message = "video encoder flush failed";
        }
        av_packet_free(&pkt);
        av_frame_free(&frame);
        if (!error_message.empty()) {
            goto fail;
        }
    }

    // ---- Audio: slightly longer than video, different time base ---------
    {
        AVPacket* pkt = av_packet_alloc();
        AVFrame* frame = av_frame_alloc();
        const int frame_size = actx->frame_size > 0 ? actx->frame_size : 1024;
        std::uint32_t rng = o.seed ^ 0xA5A5u;
        std::int64_t total_samples = 0;
        const std::int64_t target =
            static_cast<std::int64_t>(o.audio_duration_seconds * o.sample_rate);
        std::int64_t pts =
            static_cast<std::int64_t>(o.audio_start_seconds * o.sample_rate);
        while (total_samples < target) {
            const int n = static_cast<int>(
                std::min<std::int64_t>(frame_size, target - total_samples));
            frame->nb_samples = n;
            frame->format = actx->sample_fmt;
            av_channel_layout_copy(&frame->ch_layout, &actx->ch_layout);
            av_frame_get_buffer(frame, 0);
            av_frame_make_writable(frame);
            for (int ch = 0; ch < o.audio_channels; ++ch) {
                float* dst = reinterpret_cast<float*>(frame->data[ch]);
                for (int s = 0; s < n; ++s) {
                    const std::uint32_t r = nextLcg(rng);
                    dst[s] = (static_cast<double>(r & 0xFFFF) / 32768.0 - 1.0) * 0.05;
                }
            }
            frame->pts = pts;
            if (!encodeFrame(actx, frame, pkt, fmt, ast)) {
                error_message = "audio encode failed";
                break;
            }
            pts += n;
            total_samples += n;
            av_frame_unref(frame);
        }
        if (error_message.empty() && !encodeFrame(actx, nullptr, pkt, fmt, ast)) {
            error_message = "audio encoder flush failed";
        }
        av_packet_free(&pkt);
        av_frame_free(&frame);
        if (!error_message.empty()) {
            goto fail;
        }
    }

    if (av_write_trailer(fmt) < 0) {
        error_message = "write trailer failed";
        goto fail;
    }
    if (!(fmt->oformat->flags & AVFMT_NOFILE)) {
        avio_closep(&fmt->pb);
    }
    avcodec_free_context(&vctx);
    avcodec_free_context(&actx);
    avformat_free_context(fmt);
    return true;

fail:
    if (fmt && !(fmt->oformat->flags & AVFMT_NOFILE) && fmt->pb) {
        avio_closep(&fmt->pb);
    }
    avcodec_free_context(&vctx);
    avcodec_free_context(&actx);
    avformat_free_context(fmt);
    return false;
}

}  // namespace pwsync
