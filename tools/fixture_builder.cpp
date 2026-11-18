#include "fixture_builder.hpp"

#include <cstring>
#include <filesystem>
#include <fstream>

namespace mp4::fix {

namespace {

void put16_local(std::vector<uint8_t>& o, uint16_t v) {
    o.push_back(uint8_t(v >> 8));
    o.push_back(uint8_t(v));
}
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

// RAII container box; the size field is patched on end().
class BoxWriter {
public:
    BoxWriter(std::vector<uint8_t>& out, const char* type,
              bool large = false, bool to_end = false)
        : buf(out), at(out.size()), large_(large), end_(to_end) {
        if (to_end) {
            put32(buf, 0);  // size=0: box runs to end of file
        } else if (large) {
            put32(buf, 1);
        } else {
            put32(buf, 0);
        }
        put_fcc(buf, type);
        if (large) put64(buf, 0);
    }
    void full(uint8_t version, uint32_t flags) {
        buf.push_back(version);
        buf.push_back(uint8_t(flags >> 16));
        buf.push_back(uint8_t(flags >> 8));
        buf.push_back(uint8_t(flags));
    }
    ~BoxWriter() {
        if (end_) return;
        uint64_t total = buf.size() - at;
        if (large_) {
            for (int i = 7; i >= 0; --i)
                buf[at + 8 + 7 - i] = uint8_t(total >> (i * 8));
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

// Minimal but structurally valid tkhd/mdhd payloads.
void tkhd_payload(std::vector<uint8_t>& o, uint32_t track_id,
                  uint32_t duration) {
    zeros(o, 8);                 // creation/modification time
    put32(o, track_id);
    put32(o, 0);                 // reserved
    put32(o, duration);
    zeros(o, 8);                 // reserved
    put16_local(o, 0);           // layer
    put16_local(o, 0);           // alternate group
    o.push_back(0);
    o.push_back(0);              // volume
    put16_local(o, 0);           // reserved
    for (int i = 0; i < 9; ++i)  // unity matrix
        put32(o, i == 0 || i == 4 || i == 8 ? 0x00010000 : 0);
    put32(o, track_id == 1 ? 0x00400000 : 0);  // width
    zeros(o, 4);                               // height
}

void mdhd_payload(std::vector<uint8_t>& o, uint32_t timescale,
                  uint32_t duration) {
    zeros(o, 8);
    put32(o, timescale);
    put32(o, duration);
    put16_local(o, 0x55c4);  // 'und' language
    put16_local(o, 0);
}
std::vector<uint8_t> trak_box(uint32_t track_id, uint32_t timescale,
                              uint32_t duration, const char* entry_type) {
    std::vector<uint8_t> o;
    {
        BoxWriter trak(o, "trak");
        {
            BoxWriter tkhd(o, "tkhd");
            zeros(o, 4);  // version+flags
            tkhd_payload(o, track_id, duration);
        }
        {
            BoxWriter mdia(o, "mdia");
            {
                BoxWriter mdhd(o, "mdhd");
                zeros(o, 4);
                mdhd_payload(o, timescale, duration);
            }
            {
                BoxWriter hdlr(o, "hdlr");
                zeros(o, 4);
                put_fcc(o, track_id == 1 ? "vide" : "soun");
                zeros(o, 12);
                o.push_back(0);  // name
            }
            {
                BoxWriter minf(o, "minf");
                {
                    BoxWriter nmhd(o, "nmhd");
                    zeros(o, 4);
                }
                {
                    BoxWriter stbl(o, "stbl");
                    {
                        BoxWriter stsd(o, "stsd");
                        zeros(o, 4);            // version+flags
                        put32(o, 1);            // entry count
                        put32(o, 8 + 16);       // entry size
                        put_fcc(o, entry_type);
                        zeros(o, 16);           // deterministic config bytes
                    }
                }
            }
        }
    }
    return o;
}

}  // namespace

std::vector<uint8_t> build_init(const SampleSpec& spec) {
    std::vector<uint8_t> o;
    {
        BoxWriter ftyp(o, "ftyp");
        put_fcc(o, "iso5");
        put32(o, 0x200);
        put_fcc(o, "iso5");
        put_fcc(o, "iso6");
        put_fcc(o, "mp41");
    }
    uint32_t v_dur = spec.video_samples * spec.video_duration * 6;
    uint32_t a_dur = spec.audio_samples * spec.audio_duration * 6;
    {
        BoxWriter moov(o, "moov");
        {
            BoxWriter mvhd(o, "mvhd");
            zeros(o, 4);
            zeros(o, 8);
            put32(o, 1000);
            put32(o, v_dur);
            put32(o, 0x00010000);
            put16_local(o, 0x0100);
            zeros(o, 10);
            for (int i = 0; i < 9; ++i)
                put32(o, i == 0 || i == 4 || i == 8 ? 0x00010000 : 0);
            zeros(o, 24);
            put32(o, 2);
        }
        auto vtrak = trak_box(1, 90000, v_dur, "avc1");
        o.insert(o.end(), vtrak.begin(), vtrak.end());
        auto atrak = trak_box(2, 48000, a_dur, "mp4a");
        o.insert(o.end(), atrak.begin(), atrak.end());
        {
            BoxWriter mvex(o, "mvex");
            for (uint32_t id : {1u, 2u}) {
                BoxWriter trex(o, "trex");
                zeros(o, 4);
                put32(o, id);
                put32(o, 1);
                put32(o, id == 1 ? spec.video_duration
                                 : spec.audio_duration);
                put32(o, id == 1 ? spec.video_size : spec.audio_size);
                put32(o, id == 1 ? 0x01000000 : 0x02000000);
            }
        }
    }
    return o;
}

}  // namespace mp4::fix
