#include "rewriter.hpp"
#include "rewriter_detail.hpp"

#include <cstring>

namespace mp4 {

namespace {

constexpr uint32_t TFHD_BASE_DATA_OFFSET = 0x000001;
constexpr uint32_t TFHD_DEFAULT_BASE_IS_MOOF = 0x020000;
constexpr uint32_t TRUN_DATA_OFFSET = 0x000001;

}  // namespace

void put_u32(std::vector<uint8_t>& o, uint32_t v) {
    o.push_back(uint8_t(v >> 24));
    o.push_back(uint8_t(v >> 16));
    o.push_back(uint8_t(v >> 8));
    o.push_back(uint8_t(v));
}
void put_u64(std::vector<uint8_t>& o, uint64_t v) {
    for (int i = 7; i >= 0; --i) o.push_back(uint8_t(v >> (i * 8)));
}
void put_fourcc(std::vector<uint8_t>& o, const FourCC& t) {
    o.insert(o.end(), t.v, t.v + 4);
}
void put_fullbox_header(std::vector<uint8_t>& o, const FourCC& t,
                        uint8_t ver, uint32_t flags) {
    put_u32(o, 0);  // size placeholder (rewritten output always uses 32-bit)
    put_fourcc(o, t);
    o.push_back(ver);
    o.push_back(uint8_t(flags >> 16));
    o.push_back(uint8_t(flags >> 8));
    o.push_back(uint8_t(flags));
}
void patch_box_size(std::vector<uint8_t>& o, size_t at) {
    uint32_t sz = uint32_t(o.size() - at);
    o[at] = uint8_t(sz >> 24);
    o[at + 1] = uint8_t(sz >> 16);
    o[at + 2] = uint8_t(sz >> 8);
    o[at + 3] = uint8_t(sz);
}

void write_mfhd_box(std::vector<uint8_t>& o, uint32_t sequence) {
    size_t at = o.size();
    put_fullbox_header(o, mk4("mfhd"), 0, 0);
    put_u32(o, sequence);
    patch_box_size(o, at);
}

void write_tfhd_box(std::vector<uint8_t>& o, const TrackFragment& tf,
                    const std::vector<uint8_t>& src, uint64_t new_base) {
    const Box* tfhd = nullptr;
    for (const Box& b : tf.traf->children)
        if (b.type == "tfhd") tfhd = &b;
    Cursor c(src, tfhd->payload_offset(), tfhd->offset + tfhd->size);
    uint32_t flags = 0;
    c.fullbox(flags);
    uint32_t track_id = c.u32();
    uint64_t dur = 0;
    uint32_t sz = 0, fl = 0;
    bool has_bdo = (flags & TFHD_BASE_DATA_OFFSET) != 0;
    if (has_bdo) c.u64();
    if (flags & 0x000002) dur = c.u32();
    if (flags & 0x000008) sz = c.u32();
    if (flags & 0x000020) fl = c.u32();

    size_t at = o.size();
    put_fullbox_header(o, mk4("tfhd"), 0, flags);
    put_u32(o, track_id);
    if (has_bdo) put_u64(o, new_base);
    if (flags & 0x000002) put_u32(o, uint32_t(dur));
    if (flags & 0x000008) put_u32(o, sz);
    if (flags & 0x000020) put_u32(o, fl);
    if ((flags & TFHD_DEFAULT_BASE_IS_MOOF) && !has_bdo) {
        // implicit base == moof start; nothing to emit
    }
    patch_box_size(o, at);
}

void write_tfdt_box(std::vector<uint8_t>& o, uint64_t dts) {
    size_t at = o.size();
    if (dts <= UINT32_MAX) {
        put_fullbox_header(o, mk4("tfdt"), 0, 0);
        put_u32(o, uint32_t(dts));
    } else {
        put_fullbox_header(o, mk4("tfdt"), 1, 0);
        put_u64(o, dts);
    }
    patch_box_size(o, at);
}

void write_trun_box(std::vector<uint8_t>& o, const Box& trun,
                    const std::vector<uint8_t>& src, int32_t offset_shift) {
    Cursor c(src, trun.payload_offset(), trun.offset + trun.size);
    uint32_t flags = 0;
    uint8_t ver = c.fullbox(flags);
    uint32_t count = c.u32();
    bool has_off = (flags & TRUN_DATA_OFFSET) != 0;
    int32_t data_offset = has_off ? int32_t(c.u32()) : 0;
    uint32_t first_flags = 0;
    if (flags & 0x000004) first_flags = c.u32();

    size_t at = o.size();
    put_fullbox_header(o, mk4("trun"), ver, flags);
    put_u32(o, count);
    if (has_off) {
        int64_t v = int64_t(data_offset) + offset_shift;
        if (v < INT32_MIN || v > INT32_MAX)
            throw RewriteError("trun data offset overflows 32 bits");
        put_u32(o, uint32_t(int32_t(v)));
    }
    if (flags & 0x000004) put_u32(o, first_flags);
    o.insert(o.end(), src.begin() + c.pos(),
             src.begin() + trun.offset + trun.size);
    patch_box_size(o, at);
}

uint64_t fragment_duration(const TrackFragment& tf) {
    uint64_t sum = 0;
    for (const SampleInfo& s : tf.samples) sum = checked_add(sum, s.duration);
    return sum;
}

std::string pad_sequence(uint32_t sequence) {
    std::string s = std::to_string(sequence);
    return std::string(s.size() < 4 ? 4 - s.size() : 0, '0') + s;
}

void write_box(std::vector<uint8_t>& out, const Box& box,
               const std::vector<uint8_t>& source) {
    out.insert(out.end(), source.begin() + box.offset,
               source.begin() + box.offset + box.size);
}

bool verify_output_bytes(const std::vector<uint8_t>& bytes, const Limits& lim) {
    std::vector<Box> boxes = parse_boxes(bytes, {lim.max_file_size});
    if (find_box(boxes, "moov")) return true;
    const Box* moof = find_box(boxes, "moof");
    const Box* mdat = find_box(boxes, "mdat");
    if (!moof || !mdat) return false;
    if (find_child(*moof, "mfhd") == nullptr) return false;
    for (const Box* traf : find_children(*moof, "traf")) {
        if (find_child(*traf, "tfhd") == nullptr) return false;
        if (find_children(*traf, "trun").empty()) return false;
    }
    return true;
}

}  // namespace mp4
