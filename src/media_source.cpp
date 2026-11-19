#include "media_source.hpp"

#include <cmath>
#include <thread>

extern "C" {
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/dict.h>
#include <libavutil/imgutils.h>
#include <libavutil/opt.h>
#include <libavutil/rational.h>
#include <libswresample/swresample.h>
}

namespace avsync {

namespace {

// Deterministic LCG so sample generation is reproducible per seed.
struct Rng {
    uint32_t state;
    explicit Rng(uint32_t seed) : state(seed ? seed : 1u) {}
    uint32_t next() {
        state = state * 1103515245u + 12345u;
        return (state >> 16) & 0x7fff;
    }
};

// RAII guards keep every FFmpeg cleanup path exception/early-return safe.
struct FmtCtxGuard {
    AVFormatContext* ctx = nullptr;
    ~FmtCtxGuard() {
        if (ctx) {
            if (ctx->pb) avio_closep(&ctx->pb);
            avformat_free_context(ctx);
        }
    }
};
struct CodecCtxGuard {
    AVCodecContext* ctx = nullptr;
    ~CodecCtxGuard() { if (ctx) avcodec_free_context(&ctx); }
};
struct FrameGuard {
    AVFrame* f = nullptr;
    ~FrameGuard() { if (f) av_frame_free(&f); }
};
struct PktGuard {
    AVPacket* p = nullptr;
    PktGuard() : p(av_packet_alloc()) {}
    ~PktGuard() { if (p) av_packet_free(&p); }
};

// Drain a fully flushed encoder and mux every packet (rescaled to stream tb).
int drain_encoder(AVCodecContext* enc, AVFormatContext* fmt, AVStream* st,
                  AVPacket* pkt) {
    int rc = avcodec_send_frame(enc, nullptr);
    if (rc < 0) return rc;
    while (true) {
        rc = avcodec_receive_packet(enc, pkt);
        if (rc == AVERROR_EOF || rc == AVERROR(EAGAIN)) return 0;
        if (rc < 0) return rc;
        av_packet_rescale_ts(pkt, enc->time_base, st->time_base);
        pkt->stream_index = st->index;
        rc = av_interleaved_write_frame(fmt, pkt);
        if (rc < 0) return rc;
    }
}

}  // namespace

bool generate_sample(const SampleConfig& cfg, std::string& err) {
    if (cfg.duration_s <= 0 || cfg.fps_num <= 0 || cfg.fps_den <= 0 ||
        cfg.sample_rate <= 0 || cfg.path.empty()) {
        err = "generate_sample: invalid configuration";
        return false;
    }
    FmtCtxGuard fmt_guard;
    AVFormatContext* fmt = nullptr;
    int rc = avformat_alloc_output_context2(&fmt_guard.ctx, nullptr, "matroska",
                                            cfg.path.c_str());
    fmt = fmt_guard.ctx;
    if (rc < 0 || !fmt) {
        err = "avformat_alloc_output_context2 failed";
        return false;
    }

    // ---- Video stream: mpeg4 with B-frames, integer-ms time base. ----
    const AVCodec* venc = avcodec_find_encoder(AV_CODEC_ID_MPEG4);
    if (!venc) { err = "mpeg4 encoder unavailable"; return false; }
    CodecCtxGuard vguard;
    vguard.ctx = avcodec_alloc_context3(venc);
    AVCodecContext* vctx = vguard.ctx;
    vctx->width = 160;
    vctx->height = 120;
    vctx->pix_fmt = AV_PIX_FMT_YUV420P;
    vctx->time_base = AVRational{1, 1000};
    vctx->max_b_frames = 2;  // forces real decode-order reordering
    if (fmt->oformat->flags & AVFMT_GLOBALHEADER)
        vctx->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;
    if ((rc = avcodec_open2(vctx, venc, nullptr)) < 0) {
        err = "open video encoder failed";
        return false;
    }
    AVStream* vst = avformat_new_stream(fmt, nullptr);
    if (!vst) { err = "avformat_new_stream(video) failed"; return false; }
    if ((rc = avcodec_parameters_from_context(vst->codecpar, vctx)) < 0) {
        err = "video codecpar copy failed";
        return false;
    }
    vst->time_base = vctx->time_base;

    // ---- Audio stream: pcm_s16le stereo. ----
    const AVCodec* aenc = avcodec_find_encoder(AV_CODEC_ID_PCM_S16LE);
    if (!aenc) { err = "pcm_s16le encoder unavailable"; return false; }
    CodecCtxGuard aguard;
    aguard.ctx = avcodec_alloc_context3(aenc);
    AVCodecContext* actx = aguard.ctx;
    actx->sample_rate = cfg.sample_rate;
    actx->sample_fmt = AV_SAMPLE_FMT_S16;
    actx->ch_layout = AV_CHANNEL_LAYOUT_STEREO;
    // Match the container's millisecond time base so pts rescaling cannot
    // distort the audio timeline relative to video.
    actx->time_base = AVRational{1, 1000};
    if ((rc = avcodec_open2(actx, aenc, nullptr)) < 0) {
        err = "open audio encoder failed";
        return false;
    }
    AVStream* ast = avformat_new_stream(fmt, nullptr);
    if (!ast) { err = "avformat_new_stream(audio) failed"; return false; }
    if ((rc = avcodec_parameters_from_context(ast->codecpar, actx)) < 0) {
        err = "audio codecpar copy failed";
        return false;
    }
    ast->time_base = actx->time_base;

    // Bit-exact mode suppresses non-deterministic container data (e.g. the
    // matroska wall-clock DateUTC element); combined with the pinned creation
    // time below, identical seeds yield byte-identical samples.
    fmt->flags |= AVFMT_FLAG_BITEXACT;
    av_dict_set(&fmt->metadata, "creation_time",
                "2000-01-01T00:00:00.000000Z", 0);
    if (!(fmt->oformat->flags & AVFMT_NOFILE)) {
        if ((rc = avio_open(&fmt->pb, cfg.path.c_str(), AVIO_FLAG_WRITE)) < 0) {
            err = "avio_open failed for sample file";
            return false;
        }
    }
    if ((rc = avformat_write_header(fmt, nullptr)) < 0) {
        err = "avformat_write_header failed";
        return false;
    }

    FrameGuard vf, af;
    vf.f = av_frame_alloc();
    af.f = av_frame_alloc();
    PktGuard pkt;
    Rng rng(cfg.seed);
    const int64_t dur_us = llround(cfg.duration_s * 1'000'000.0);
    const int64_t off_us = cfg.start_offset_us;

    // Encode one video frame at pts_us (media timeline, includes start offset).
    auto send_video = [&](int64_t pts_us, uint32_t index) -> int {
        AVFrame* f = vf.f;
        f->format = vctx->pix_fmt;
        f->width = vctx->width;
        f->height = vctx->height;
        if ((rc = av_frame_get_buffer(f, 0)) < 0) return rc;
        if ((rc = av_frame_make_writable(f)) < 0) return rc;
        // Deterministic luma pattern from frame index + seed; no external data.
        for (int y = 0; y < f->height; ++y)
            for (int x = 0; x < f->width; ++x)
                f->data[0][y * f->linesize[0] + x] =
                    static_cast<uint8_t>((index * 7 + x + y + rng.next()) & 0xff);
        for (int c = 1; c < 3; ++c)
            for (int y = 0; y < f->height / 2; ++y)
                for (int x = 0; x < f->width / 2; ++x)
                    f->data[c][y * f->linesize[c] + x] = 128;
        f->pts = pts_us / 1000;  // encoder time base is 1/1000 s
        rc = avcodec_send_frame(vctx, f);
        av_frame_unref(f);
        if (rc < 0) return rc;
        while (true) {
            rc = avcodec_receive_packet(vctx, pkt.p);
            if (rc == AVERROR(EAGAIN) || rc == AVERROR_EOF) return 0;
            if (rc < 0) return rc;
            av_packet_rescale_ts(pkt.p, vctx->time_base, vst->time_base);
            pkt.p->stream_index = vst->index;
            if ((rc = av_interleaved_write_frame(fmt, pkt.p)) < 0) return rc;
        }
    };

    // Encode one fixed-size audio frame at pts_us.
    auto send_audio = [&](int64_t pts_us, int frame_samples) -> int {
        AVFrame* f = af.f;
        f->format = actx->sample_fmt;
        f->nb_samples = frame_samples;
        f->ch_layout = actx->ch_layout;
        if ((rc = av_frame_get_buffer(f, 0)) < 0) return rc;
        if ((rc = av_frame_make_writable(f)) < 0) return rc;
        auto* out = reinterpret_cast<int16_t*>(f->data[0]);
        const double t0 = pts_us / 1'000'000.0;
        for (int i = 0; i < frame_samples; ++i) {
            double t = t0 + static_cast<double>(i) / cfg.sample_rate;
            int s = static_cast<int>(
                8000.0 * std::sin(2.0 * M_PI * 440.0 * t) +
                (static_cast<int>(rng.next()) - 0x3fff) / 64);
            s = std::max(-32768, std::min(32767, s));
            out[2 * i] = static_cast<int16_t>(s);
            out[2 * i + 1] = static_cast<int16_t>(s);
        }
        f->pts = pts_us / 1000;  // encoder time base is 1/1000 s
        rc = avcodec_send_frame(actx, f);
        av_frame_unref(f);
        if (rc < 0) return rc;
        while (true) {
            rc = avcodec_receive_packet(actx, pkt.p);
            if (rc == AVERROR(EAGAIN) || rc == AVERROR_EOF) return 0;
            if (rc < 0) return rc;
            av_packet_rescale_ts(pkt.p, actx->time_base, ast->time_base);
            pkt.p->stream_index = ast->index;
            if ((rc = av_interleaved_write_frame(fmt, pkt.p)) < 0) return rc;
        }
    };

    // Interleaved scheduler: always emit the stream with the earlier media pts.
    const double frame_dur_s =
        static_cast<double>(cfg.fps_den) / cfg.fps_num;
    int64_t vpts = off_us;
    int64_t apts = off_us;
    uint32_t vindex = 0;
    const int audio_frame = 1024;
    const int64_t v_end = off_us + dur_us;
    const int64_t a_end = off_us + dur_us;
    while (vpts < v_end || apts < a_end) {
        bool video_turn = apts >= a_end || (vpts < v_end && vpts <= apts);
        if (video_turn) {
            if ((rc = send_video(vpts, vindex++)) < 0) {
                err = "video encode failed";
                return false;
            }
            // Variable frame rate: deterministic +/-8% slot jitter per seed.
            double jitter = 1.0 + (static_cast<int>(rng.next() & 7) - 4) * 0.04;
            vpts += std::max<int64_t>(1, llround(frame_dur_s * jitter * 1e6));
        } else {
            int n = std::min<int64_t>(
                audio_frame, (a_end - apts) * cfg.sample_rate / 1'000'000);
            if (n <= 0) break;
            if ((rc = send_audio(apts, n)) < 0) {
                err = "audio encode failed";
                return false;
            }
            apts += static_cast<int64_t>(n) * 1'000'000 / cfg.sample_rate;
        }
    }
    if ((rc = drain_encoder(vctx, fmt, vst, pkt.p)) < 0 ||
        (rc = drain_encoder(actx, fmt, ast, pkt.p)) < 0) {
        err = "encoder drain failed";
        return false;
    }
    if ((rc = av_write_trailer(fmt)) < 0) {
        err = "av_write_trailer failed";
        return false;
    }
    return true;
}

namespace {

// Convert any decoded audio layout/format to interleaved stereo s16.
class AudioConverter {
public:
    bool open(AVCodecContext* dec, int dst_rate, std::string& err) {
        swr_ = swr_alloc();
        if (!swr_) { err = "swr_alloc failed"; return false; }
        AVChannelLayout stereo = AV_CHANNEL_LAYOUT_STEREO;
        av_opt_set_chlayout(swr_, "in_chlayout", &dec->ch_layout, 0);
        av_opt_set_int(swr_, "in_sample_rate", dec->sample_rate, 0);
        av_opt_set_sample_fmt(swr_, "in_sample_fmt", dec->sample_fmt, 0);
        av_opt_set_chlayout(swr_, "out_chlayout", &stereo, 0);
        av_opt_set_int(swr_, "out_sample_rate", dst_rate, 0);
        av_opt_set_sample_fmt(swr_, "out_sample_fmt", AV_SAMPLE_FMT_S16, 0);
        if (swr_init(swr_) < 0) { err = "swr_init failed"; return false; }
        rate_ = dst_rate;
        return true;
    }
    ~AudioConverter() { if (swr_) swr_free(&swr_); }

    // Returns number of converted samples per channel, or -1 on error.
    int convert(const AVFrame* f, std::vector<int16_t>& out) {
        int cap = swr_get_out_samples(swr_, f->nb_samples);
        if (cap < 0) return -1;
        out.resize(static_cast<size_t>(cap) * 2);
        uint8_t* dst[1] = {reinterpret_cast<uint8_t*>(out.data())};
        int n = swr_convert(swr_, dst, cap,
                            const_cast<const uint8_t**>(f->extended_data),
                            f->nb_samples);
        if (n < 0) return -1;
        out.resize(static_cast<size_t>(n) * 2);
        return n;
    }
    int rate() const { return rate_; }

private:
    SwrContext* swr_ = nullptr;
    int rate_ = 0;
};

int64_t frame_pts_us(const AVFrame* f, const AVRational& tb) {
    if (f->pts == AV_NOPTS_VALUE) return 0;
    return av_rescale_q(f->pts, tb, AVRational{1, 1'000'000});
}

}  // namespace

DecodeResult decode_into_queues(const std::string& path,
                                const InjectionConfig& inj,
                                BoundedQueue<AudioChunk>& audio_q,
                                BoundedQueue<VideoFrameMsg>& video_q,
                                std::atomic<bool>* stop) {
    DecodeResult res;
    AVFormatContext* fmt = nullptr;
    int rc = avformat_open_input(&fmt, path.c_str(), nullptr, nullptr);
    if (rc < 0) {
        res.error = "avformat_open_input failed";
        return res;
    }
    if ((rc = avformat_find_stream_info(fmt, nullptr)) < 0) {
        res.error = "avformat_find_stream_info failed";
        avformat_close_input(&fmt);
        return res;
    }
    int vi = av_find_best_stream(fmt, AVMEDIA_TYPE_VIDEO, -1, -1, nullptr, 0);
    int ai = av_find_best_stream(fmt, AVMEDIA_TYPE_AUDIO, -1, -1, nullptr, 0);
    if (vi < 0 || ai < 0) {
        res.error = "sample must contain both audio and video streams";
        avformat_close_input(&fmt);
        return res;
    }
    res.start_time_us =
        fmt->start_time != AV_NOPTS_VALUE
            ? fmt->start_time  // AV_TIME_BASE_Q is microseconds
            : 0;

    const AVCodec* vcodec = avcodec_find_decoder(
        fmt->streams[vi]->codecpar->codec_id);
    const AVCodec* acodec = avcodec_find_decoder(
        fmt->streams[ai]->codecpar->codec_id);
    CodecCtxGuard vg, ag;
    vg.ctx = avcodec_alloc_context3(vcodec);
    ag.ctx = avcodec_alloc_context3(acodec);
    if (!vg.ctx || !ag.ctx ||
        avcodec_parameters_to_context(vg.ctx, fmt->streams[vi]->codecpar) < 0 ||
        avcodec_parameters_to_context(ag.ctx, fmt->streams[ai]->codecpar) < 0 ||
        avcodec_open2(vg.ctx, vcodec, nullptr) < 0 ||
        avcodec_open2(ag.ctx, acodec, nullptr) < 0) {
        res.error = "decoder setup failed";
        avformat_close_input(&fmt);
        return res;
    }
    AudioConverter conv;
    if (!conv.open(ag.ctx, ag.ctx->sample_rate, res.error)) {
        avformat_close_input(&fmt);
        return res;
    }
    AVRational vtb = fmt->streams[vi]->time_base;
    AVRational atb = fmt->streams[ai]->time_base;

    AVPacket* pkt = av_packet_alloc();
    AVFrame* frame = av_frame_alloc();
    if (!pkt || !frame) {
        res.error = "av packet/frame alloc failed";
        if (pkt) av_packet_free(&pkt);
        if (frame) av_frame_free(&frame);
        avformat_close_input(&fmt);
        return res;
    }
    {
    // Abortable pushes: if a consumer dies the queue would stay full forever,
    // so poll an external stop flag while waiting for room.
    auto push_video = [&](VideoFrameMsg m) -> bool {        for (;;) {
            bool timed_out = false;
            if (video_q.push_for(std::move(m), std::chrono::milliseconds(50),
                                 timed_out))
                return true;
            if (!timed_out) { res.error = "video queue closed"; return false; }
            if (stop && stop->load(std::memory_order_relaxed)) {
                res.error = "decode aborted by shutdown";
                return false;
            }
        }
    };
    auto push_audio = [&](AudioChunk c) -> bool {
        for (;;) {
            bool timed_out = false;
            if (audio_q.push_for(std::move(c), std::chrono::milliseconds(50),
                                 timed_out))
                return true;
            if (!timed_out) { res.error = "audio queue closed"; return false; }
            if (stop && stop->load(std::memory_order_relaxed)) {
                res.error = "decode aborted by shutdown";
                return false;
            }
        }
    };
    while (true) {
        rc = av_read_frame(fmt, pkt);
        if (rc == AVERROR_EOF) {
            avcodec_send_packet(vg.ctx, nullptr);
            avcodec_send_packet(ag.ctx, nullptr);
            rc = 0;
            break;
        }
        if (rc < 0) { res.error = "av_read_frame failed"; goto decode_failed; }
        AVCodecContext* dec =
            pkt->stream_index == vi ? vg.ctx
            : pkt->stream_index == ai ? ag.ctx : nullptr;
        if (dec && (rc = avcodec_send_packet(dec, pkt)) < 0) {
            res.error = "avcodec_send_packet failed";
            av_packet_unref(pkt);
            goto decode_failed;
        }
        av_packet_unref(pkt);
        if (!dec) continue;

        while (true) {
            rc = avcodec_receive_frame(dec, frame);
            if (rc == AVERROR(EAGAIN) || rc == AVERROR_EOF) { rc = 0; break; }
            if (rc < 0) { res.error = "avcodec_receive_frame failed"; goto decode_failed; }
            if (dec == vg.ctx) {
                // Decode-latency spike on every 7th presentation frame.
                if (inj.video_delay_ms > 0 &&
                    res.video_frames % 7 == 6)
                    std::this_thread::sleep_for(
                        std::chrono::milliseconds(inj.video_delay_ms));
                VideoFrameMsg msg;
                msg.index = res.video_frames;
                msg.width = frame->width;
                msg.height = frame->height;
                msg.pts_valid = frame->pts != AV_NOPTS_VALUE;
                msg.pts_us = msg.pts_valid ? frame_pts_us(frame, vtb) : 0;
                bool blank = inj.missing_pts_every > 0 &&
                             res.video_frames > 0 &&
                             res.video_frames % inj.missing_pts_every == 0;
                if (blank) {
                    msg.pts_valid = false;
                    msg.pts_us = 0;
                    ++res.injected_missing_pts;
                }
                if (!push_video(msg)) goto decode_failed;
                ++res.video_frames;
                if (inj.duplicate_every > 0 &&
                    res.video_frames % inj.duplicate_every == 0) {
                    if (!push_video(msg)) goto decode_failed;
                    ++res.injected_duplicates;
                    ++res.video_frames;
                }
            } else {
                AudioChunk chunk;
                chunk.pts_valid = frame->pts != AV_NOPTS_VALUE;
                chunk.pts_us = chunk.pts_valid ? frame_pts_us(frame, atb) : 0;
                chunk.sample_rate = conv.rate();
                chunk.channels = 2;
                int n = conv.convert(frame, chunk.interleaved);
                if (n < 0) { res.error = "swr_convert failed"; goto decode_failed; }
                chunk.nb_samples = n;
                if (!push_audio(std::move(chunk))) goto decode_failed;
                res.audio_samples += n;
            }
            av_frame_unref(frame);
        }
        if (rc < 0) goto decode_failed;
    }

    // Flush both decoders after the EOF packet was signalled above.
    for (AVCodecContext* dec : {static_cast<AVCodecContext*>(vg.ctx),
                                static_cast<AVCodecContext*>(ag.ctx)}) {
        while (true) {
            int r2 = avcodec_receive_frame(dec, frame);
            if (r2 == AVERROR_EOF || r2 == AVERROR(EAGAIN)) break;
            if (r2 < 0) { res.error = "decoder flush failed"; goto decode_failed; }
            if (dec == vg.ctx) {
                VideoFrameMsg msg;
                msg.index = res.video_frames;
                msg.width = frame->width;
                msg.height = frame->height;
                msg.pts_valid = frame->pts != AV_NOPTS_VALUE;
                msg.pts_us = msg.pts_valid ? frame_pts_us(frame, vtb) : 0;
                bool blank = inj.missing_pts_every > 0 &&
                             res.video_frames > 0 &&
                             res.video_frames % inj.missing_pts_every == 0;
                if (blank) {
                    msg.pts_valid = false;
                    ++res.injected_missing_pts;
                }
                if (!push_video(msg)) goto decode_failed;
                ++res.video_frames;
            } else {
                AudioChunk chunk;
                chunk.pts_valid = frame->pts != AV_NOPTS_VALUE;
                chunk.pts_us = chunk.pts_valid ? frame_pts_us(frame, atb) : 0;
                chunk.sample_rate = conv.rate();
                chunk.channels = 2;
                int n = conv.convert(frame, chunk.interleaved);
                if (n < 0) { res.error = "swr_convert failed"; goto decode_failed; }
                chunk.nb_samples = n;
                if (!push_audio(std::move(chunk))) goto decode_failed;
                res.audio_samples += n;
            }
            av_frame_unref(frame);
        }
    }
    res.ok = true;
decode_failed:
    ;
    }  // end decode block (all gotos stay inside this scope)

    if (pkt) av_packet_free(&pkt);
    if (frame) av_frame_free(&frame);
    avformat_close_input(&fmt);
    return res;
}

}  // namespace avsync
