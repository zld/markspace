#include "box_reader.hpp"

#include <cstring>
#include <fstream>
#include <set>

namespace mp4 {

std::string FourCC::str() const { return std::string(v, 4); }

bool FourCC::operator==(const char* code) const {
    return std::memcmp(v, code, 4) == 0;
}

void Cursor::need(uint64_t n) const {
    if (p > limit || n > limit - p)
        throw BoxError("read past box boundary");
}
void Cursor::skip(uint64_t n) {
    need(n);
    p += n;
}
void Cursor::seek(uint64_t pos) {
    if (pos > limit) throw BoxError("seek past box boundary");
    p = pos;
}
uint8_t Cursor::u8() {
    need(1);
    return data[p++];
}
uint16_t Cursor::u16() {
    need(2);
    uint16_t x = uint16_t((uint16_t(data[p]) << 8) | data[p + 1]);
    p += 2;
    return x;
}
uint32_t Cursor::u32() {
    need(4);
    uint32_t x = 0;
    for (int i = 0; i < 4; ++i) x = (x << 8) | data[p + i];
    p += 4;
    return x;
}
uint64_t Cursor::u64() {
    need(8);
    uint64_t x = 0;
    for (int i = 0; i < 8; ++i) x = (x << 8) | data[p + i];
    p += 8;
    return x;
}
FourCC Cursor::fourcc() {
    need(4);
    FourCC t;
    std::memcpy(t.v, &data[p], 4);
    p += 4;
    return t;
}
uint8_t Cursor::fullbox(uint32_t& flags) {
    need(4);
    uint8_t ver = data[p];
    flags = (uint32_t(data[p + 1]) << 16) | (uint32_t(data[p + 2]) << 8) |
            data[p + 3];
    p += 4;
    return ver;
}
std::vector<uint8_t> Cursor::bytes(uint64_t n) {
    need(n);
    std::vector<uint8_t> out(data.begin() + p, data.begin() + p + n);
    p += n;
    return out;
}

std::vector<uint8_t> read_file(const std::string& path,
                               const ParseOptions& opt) {
    std::ifstream f(path, std::ios::binary | std::ios::ate);
    if (!f) throw FileError("cannot open file: " + path);
    std::streamoff len = f.tellg();
    if (len < 0) throw FileError("cannot stat file: " + path);
    uint64_t n = uint64_t(len);
    if (n > opt.max_file_size)
        throw BoxError("file exceeds 2 GiB limit: " + path);
    f.seekg(0);
    std::vector<uint8_t> buf(n);
    if (n && !f.read(reinterpret_cast<char*>(buf.data()),
                     static_cast<std::streamsize>(n)))
        throw FileError("short read: " + path);
    return buf;
}

namespace {

const std::set<std::string>& containers() {
    static const std::set<std::string> c = {
        "moov", "trak", "mdia", "minf", "dinf", "stbl", "edts",
        "udta", "meco", "moof", "traf", "mvex", "mfra", "sinf", "schi", "iref"};
    return c;
}

const std::set<std::string>& known_boxes() {
    static const std::set<std::string> c = {
        "ftyp", "free", "skip", "mdat", "mvhd", "tkhd", "mdhd", "hdlr",
        "vmhd", "smhd", "nmhd", "elng", "dref", "url",  "urn",  "stsd",
        "stts", "ctts", "stsc", "stsz", "stz2", "stco", "co64", "stss",
        "elst", "mehd", "trex", "mfhd", "tfhd", "tfdt", "trun", "tfra",
        "mfro", "schm", "tenc", "pasp", "kind", "stsh"};
    return c;
}

bool encrypted_box(const FourCC& t) {
    return t == "pssh" || t == "senc" || t == "saiz" || t == "saio";
}

bool encrypted_entry(const FourCC& t) {
    return t == "encv" || t == "enca" || t == "encs" || t == "encm";
}

// stsd is kept as a leaf box but its entries are scanned for encryption.
void scan_stsd(const std::vector<uint8_t>& data, const Box& b) {
    Cursor c(data, b.payload_offset(), b.offset + b.size);
    uint32_t flags = 0;
    c.fullbox(flags);
    uint32_t count = c.u32();
    for (uint32_t i = 0; i < count; ++i) {
        uint64_t es = c.pos();
        uint32_t entry_size = c.u32();
        FourCC et = c.fourcc();
        if (encrypted_entry(et))
            throw BoxError("encrypted sample entry '" + et.str() +
                           "' is not supported");
        if (entry_size < 8 || es + entry_size > b.offset + b.size)
            throw BoxError("truncated stsd sample entry");
        c.seek(es + entry_size);
    }
}

Box parse_one(Cursor& cur, const ParseOptions& opt, bool top_level,
              uint64_t file_size) {
    uint64_t start = cur.pos();
    if (cur.remaining() < 8) throw BoxError("truncated box header");
    uint32_t size32 = cur.u32();
    FourCC type = cur.fourcc();
    uint64_t hdr = 8;
    uint64_t size = size32;

    if (size32 == 1) {
        if (cur.remaining() < 8) throw BoxError("truncated largesize header");
        size = cur.u64();
        hdr = 16;
        if (size < 16) throw BoxError("largesize smaller than 16 bytes");
    } else if (size32 == 0) {
        if (!top_level)
            throw BoxError("size==0 box is only valid at file level");
        size = file_size - start;
    } else if (size32 < 8) {
        throw BoxError("box size smaller than 8 bytes: " + type.str());
    }

    if (size > opt.max_file_size)
        throw BoxError("box exceeds 2 GiB limit: " + type.str());
    if (size > file_size - start)
        throw BoxError("box length extends past end of file: " + type.str());

    if (encrypted_box(type))
        throw BoxError("encrypted box '" + type.str() +
                       "' is not supported; refusing to repair protected media");

    Box b;
    b.type = type;
    b.offset = start;
    b.size = size;
    b.header = hdr;

    uint64_t payload_end = start + size;
    if (containers().count(type.str())) {
        Cursor child(cur.buffer(), start + hdr, payload_end);
        while (!child.eof())
            b.children.push_back(
                parse_one(child, opt, false, file_size));
    } else {
        if (type == "stsd") scan_stsd(cur.buffer(), b);
        if (!top_level && !known_boxes().count(type.str()))
            throw BoxError("unknown critical box '" + type.str() + "'");
    }
    cur.seek(payload_end);
    return b;
}

}  // namespace

std::vector<Box> parse_boxes(const std::vector<uint8_t>& data,
                             const ParseOptions& opt) {
    if (data.empty()) throw BoxError("empty file contains no boxes");
    if (data.size() > opt.max_file_size)
        throw BoxError("file exceeds 2 GiB limit");
    Cursor cur(data, 0, data.size());
    std::vector<Box> out;
    while (!cur.eof()) out.push_back(parse_one(cur, opt, true, data.size()));
    return out;
}

const Box* find_box(const std::vector<Box>& boxes, const char* type) {
    for (const Box& b : boxes)
        if (b.type == type) return &b;
    return nullptr;
}
const Box* find_child(const Box& parent, const char* type) {
    for (const Box& b : parent.children)
        if (b.type == type) return &b;
    return nullptr;
}
std::vector<const Box*> find_all(const std::vector<Box>& boxes,
                                 const char* type) {
    std::vector<const Box*> out;
    for (const Box& b : boxes)
        if (b.type == type) out.push_back(&b);
    return out;
}
std::vector<const Box*> find_children(const Box& parent, const char* type) {
    std::vector<const Box*> out;
    for (const Box& b : parent.children)
        if (b.type == type) out.push_back(&b);
    return out;
}

std::vector<uint8_t> stsd_entries_payload(
    const std::vector<uint8_t>& data, const Box& stsd) {
    Cursor c(data, stsd.payload_offset(), stsd.offset + stsd.size);
    uint32_t flags = 0;
    c.fullbox(flags);
    c.u32();  // entry_count
    std::vector<uint8_t> out = c.bytes(c.remaining());
    return out;
}

}  // namespace mp4
