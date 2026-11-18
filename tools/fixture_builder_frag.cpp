#include "fixture_builder.hpp"

#include <filesystem>
#include <fstream>

namespace mp4::fix {

namespace {

void put32(std::vector<uint8_t>& o, uint32_t v) {
    o.push_back(uint8_t(v >> 24));
    o.push_back(uint8_t(v >> 16));
    o.push_back(uint8_t(v >> 8));
    o.push_back(uint8_t(v));
}
void put64(std::vector<uint8_t>& o, uint64_t v) {
    for (int i = 7; i >= 0; --i) o.push_back(uint8_t(v >> (i * 8)));
}
void put_fcc(std::vector<uint8_t>& o, const char* t) {
    o.insert(o.end(), t, t + 4);
}
void zeros(std::vector<uint8_t>& o, size_t n) {
    o.insert(o.end(), n, 0);
}

class BoxWriter {
public:
    BoxWriter(std::vector<uint8_t>& out, const char* type,
              bool large = false, bool to_end = false)
        : buf(out), at(out.size()), large_(large), end_(to_end) {
        put32(buf, to_end ? 0 : (large ? 1 : 0));
        put_fcc(buf, type);
        if (large) put64(buf, 0);
    }
    ~BoxWriter() {
        if (end_) return;
        uint64_t total = buf.size() - at;
        if (large_) {
            for (int i = 0; i < 8; ++i)
                buf[at + 8 + i] = uint8_t(total >> ((7 - i) * 8));
        } else {
            uint32_t t = uint32_t(total);
            buf[at] = uint8_t(t >> 24);
            buf[at + 1] = uint8_t(t >> 16);
            buf[at + 2] = uint8_t(t >> 8);
            buf[at + 3] = uint8_t(t);
        }
    }

private:
    std::vector<uint8_t>& buf;
    size_t at;
    bool large_;
    bool end_;
};

void tfhd_box(std::vector<uint8_t>& o, uint32_t track_id, uint32_t duration,
              uint32_t size, uint32_t flags32, bool explicit_defaults) {
    BoxWriter w(o, "tfhd");
    uint32_t flags = 0x020000;  // default-base-is-moof
    if (explicit_defaults) flags |= 0x000002 | 0x000008 | 0x000020;
    o.push_back(0);
    o.push_back(uint8_t(flags >> 16));
    o.push_back(uint8_t(flags >> 8));
    o.push_back(uint8_t(flags));
    put32(o, track_id);
    if (explicit_defaults) {
        put32(o, duration);
        put32(o, size);
        put32(o, flags32);
    }
}

void tfdt_box(std::vector<uint8_t>& o, uint64_t base, bool use64) {
    BoxWriter w(o, "tfdt");
    if (use64 || base > UINT32_MAX) {
        o.push_back(1);
        zeros(o, 3);
        put64(o, base);
    } else {
        o.push_back(0);
        zeros(o, 3);
        put32(o, uint32_t(base));
    }
}

// One trun per traf, always with data-offset; optional per-sample fields.
void trun_box(std::vector<uint8_t>& o, uint32_t count, int32_t data_offset,
              const SampleSpec& spec, uint32_t track_id,
              bool explicit_defaults, bool negative_v1) {
    bool per_sample = !explicit_defaults;
    uint8_t version = negative_v1 ? 1 : 0;
    uint32_t flags = 0x000001;  // data-offset-present
    if (per_sample)
        flags |= 0x000100 | 0x000200 | 0x000400;  // duration/size/flags
    if (negative_v1) flags |= 0x000800;          // composition-offset
    BoxWriter w(o, "trun");
    o.push_back(version);
    o.push_back(uint8_t(flags >> 16));
    o.push_back(uint8_t(flags >> 8));
    o.push_back(uint8_t(flags));
    put32(o, count);
    put32(o, uint32_t(data_offset));
    for (uint32_t i = 0; i < count; ++i) {
        if (per_sample) {
            put32(o, track_id == 1 ? spec.video_duration
                                   : spec.audio_duration);
            put32(o, track_id == 1 ? spec.video_size : spec.audio_size);
            put32(o, track_id == 1 ? 0x01000000 : 0x02000000);
        }
        if (negative_v1) {
            // Negative composition offset on the first video sample.
            put32(o, uint32_t(track_id == 1 && i == 0 ? -3600 : 0));
        }
    }
}

void traf_box(std::vector<uint8_t>& o, uint32_t track_id, uint64_t base,
              int32_t data_offset, const SampleSpec& spec,
              const FragmentSpec& fs) {
    BoxWriter w(o, "traf");
    uint32_t n = track_id == 1 ? spec.video_samples : spec.audio_samples;
    uint32_t dur = track_id == 1 ? spec.video_duration
                                 : spec.audio_duration;
    uint32_t sz = track_id == 1 ? spec.video_size : spec.audio_size;
    uint32_t sflags = track_id == 1 ? 0x01000000 : 0x02000000;
    tfhd_box(o, track_id, dur, sz, sflags, fs.explicit_defaults);
    tfdt_box(o, base, fs.tfdt_64);
    bool neg = fs.trun_v1_negative && track_id == 1;
    trun_box(o, n, data_offset, spec, track_id, fs.explicit_defaults, neg);
}

void sample_payload(std::vector<uint8_t>& o, uint32_t count,
                    uint32_t size, uint8_t seed) {
    for (uint32_t i = 0; i < count; ++i)
        for (uint32_t b = 0; b < size; ++b)
            o.push_back(uint8_t((seed + i * 7 + b * 3) & 0xff));
}

}  // namespace

std::vector<uint8_t> build_fragment(const SampleSpec& spec,
                                    const FragmentSpec& fs) {
    std::vector<uint8_t> o;
    {
        BoxWriter moof(o, "moof", fs.extended_box_size);
        {
            BoxWriter mfhd(o, "mfhd");
            zeros(o, 4);
            put32(o, fs.sequence);
        }
        // First pass layout: data offsets are fixed-width and independent of
        // each other; video traf first, audio traf second.
        size_t moof_end_probe = 0;
        {
            std::vector<uint8_t> probe;
            BoxWriter pm(probe, "moof", fs.extended_box_size);
            BoxWriter pmm(probe, "mfhd");
            zeros(probe, 4);
            put32(probe, fs.sequence);
            traf_box(probe, 1, fs.video_base_time, 0, spec, fs);
            traf_box(probe, 2, fs.audio_base_time, 0, spec, fs);
            moof_end_probe = probe.size();
        }
        uint64_t mdat_hdr = 8;
        int32_t v_off = int32_t(moof_end_probe + mdat_hdr);
        uint64_t v_bytes = uint64_t(spec.video_samples) * spec.video_size;
        int32_t a_off = v_off + int32_t(v_bytes);
        traf_box(o, 1, fs.video_base_time, v_off, spec, fs);
        traf_box(o, 2, fs.audio_base_time, a_off, spec, fs);
    }
    {
        BoxWriter mdat(o, "mdat", false, fs.mdat_to_end);
        sample_payload(o, spec.video_samples, spec.video_size, 0x10);
        sample_payload(o, spec.audio_samples, spec.audio_size, 0x80);
    }
    return o;
}

std::vector<std::pair<std::string, std::vector<uint8_t>>>
build_normal_set(const SampleSpec& spec) {
    std::vector<std::pair<std::string, std::vector<uint8_t>>> out;
    uint64_t v_time = 0, a_time = 0;
    for (uint32_t i = 1; i <= 6; ++i) {
        FragmentSpec fs;
        fs.sequence = i;
        fs.video_base_time = v_time;
        fs.audio_base_time = a_time;
        fs.tfdt_64 = (i % 2 == 0);
        fs.trun_v1_negative = (i == 3);
        fs.explicit_defaults = (i % 2 == 0);
        fs.extended_box_size = (i == 5);
        fs.mdat_to_end = (i == 6);
        out.push_back({std::string("seg-") +
                           (i < 10 ? "0" : "") + std::to_string(i) + ".m4s",
                       build_fragment(spec, fs)});
        v_time += uint64_t(spec.video_samples) * spec.video_duration;
        a_time += uint64_t(spec.audio_samples) * spec.audio_duration;
    }
    return out;
}

void write_file(const std::string& path,
                const std::vector<uint8_t>& bytes) {
    namespace fs = std::filesystem;
    fs::create_directories(fs::path(path).parent_path());
    std::ofstream f(path, std::ios::binary | std::ios::trunc);
    if (!f) throw std::runtime_error("cannot write " + path);
    if (!bytes.empty() &&
        !f.write(reinterpret_cast<const char*>(bytes.data()),
                 static_cast<std::streamsize>(bytes.size())))
        throw std::runtime_error("short write " + path);
}

}  // namespace mp4::fix
