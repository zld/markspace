#include "fragment_index.hpp"

#include <limits>

namespace mp4 {

uint64_t checked_add(uint64_t a, uint64_t b) {
    if (a > std::numeric_limits<uint64_t>::max() - b)
        throw IndexError("integer overflow in time/offset accumulation");
    return a + b;
}

namespace {

const Box* require_child(const Box& parent, const char* type) {
    const Box* b = find_child(parent, type);
    if (!b) throw IndexError(std::string("missing required box ") + type +
                             " inside " + parent.type.str());
    return b;
}

// tkhd: FullBox; v0 has 32-bit creation/mod, v1 64-bit; track_ID follows.
uint32_t parse_track_id(const std::vector<uint8_t>& data, const Box& trak) {
    const Box& tkhd = *require_child(trak, "tkhd");
    Cursor c(data, tkhd.payload_offset(), tkhd.offset + tkhd.size);
    uint32_t flags = 0;
    uint8_t ver = c.fullbox(flags);
    if (ver == 1) c.skip(16); else c.skip(8);
    return c.u32();
}

// mdhd: v0 ... timescale(u32) duration(u32); v1 ... timescale(u32) duration(u64)
uint32_t parse_timescale(const std::vector<uint8_t>& data,
                         const Box& trak) {
    const Box& mdia = *require_child(trak, "mdia");
    const Box& mdhd = *require_child(mdia, "mdhd");
    Cursor c(data, mdhd.payload_offset(), mdhd.offset + mdhd.size);
    uint32_t flags = 0;
    uint8_t ver = c.fullbox(flags);
    if (ver == 1) c.skip(16); else c.skip(8);
    uint32_t timescale = c.u32();
    if (timescale == 0) throw IndexError("mdhd timescale must not be zero");
    return timescale;
}

// stsd -> raw sample entries (entry_count stripped), used as codec fingerprint.
std::vector<uint8_t> parse_codec_config(const std::vector<uint8_t>& data,
                                        const Box& trak) {
    const Box& mdia = *require_child(trak, "mdia");
    const Box& minf = *require_child(mdia, "minf");
    const Box& stbl = *require_child(minf, "stbl");
    const Box& stsd = *require_child(stbl, "stsd");
    return stsd_entries_payload(data, stsd);
}

struct TrexDefaults {
    uint32_t track_id;
    uint64_t duration;
    uint32_t size;
    uint32_t flags;
};

TrexDefaults parse_trex(const std::vector<uint8_t>& data, const Box& trex) {
    Cursor c(data, trex.payload_offset(), trex.offset + trex.size);
    uint32_t flags = 0;
    c.fullbox(flags);
    TrexDefaults t;
    t.track_id = c.u32();
    c.u32();  // default_sample_description_index
    t.duration = c.u32();
    t.size = c.u32();
    t.flags = c.u32();
    return t;
}

}  // namespace

MediaIndex parse_init_segment(const std::vector<uint8_t>& data,
                              const Limits& lim) {
    std::vector<Box> boxes = parse_boxes(data, {lim.max_file_size});
    const Box* moov = find_box(boxes, "moov");
    if (!moov) throw IndexError("initialization segment has no moov box");

    MediaIndex idx;
    std::vector<const Box*> traks = find_children(*moov, "trak");
    if (traks.empty()) throw IndexError("moov contains no trak boxes");
    if (traks.size() > lim.max_tracks)
        throw IndexError("track count exceeds configured maximum");

    // trex boxes live in moov/mvex, one per track.
    std::map<uint32_t, TrexDefaults> trex;
    if (const Box* mvex = find_child(*moov, "mvex"))
        for (const Box* b : find_children(*mvex, "trex")) {
            TrexDefaults t = parse_trex(data, *b);
            trex.emplace(t.track_id, t);
        }

    for (const Box* trak : traks) {
        TrackConfig cfg;
        cfg.track_id = parse_track_id(data, *trak);
        cfg.timescale = parse_timescale(data, *trak);
        cfg.codec_config = parse_codec_config(data, *trak);
        cfg.codec_config_sha256 = sha256_hex(cfg.codec_config);
        auto it = trex.find(cfg.track_id);
        if (it == trex.end())
            throw IndexError("missing trex defaults for track " +
                             std::to_string(cfg.track_id));
        cfg.default_sample_duration = it->second.duration;
        cfg.default_sample_size = it->second.size;
        cfg.default_sample_flags = it->second.flags;
        if (!idx.tracks.emplace(cfg.track_id, cfg).second)
            throw IndexError("duplicate track id " +
                             std::to_string(cfg.track_id) + " in moov");
    }
    return idx;
}

}  // namespace mp4
