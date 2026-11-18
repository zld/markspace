#pragma once

#include <cstddef>
#include <cstdint>
#include <stdexcept>
#include <string>
#include <vector>

namespace mp4 {

// Four character code, stored as four raw bytes (may contain non-ASCII).
struct FourCC {
    char v[4];
    std::string str() const;
    bool operator==(const char* code) const;
    bool operator!=(const char* code) const { return !(*this == code); }
};

// One ISO BMFF box. Offsets are absolute positions inside the file buffer.
struct Box {
    FourCC type;
    uint64_t offset = 0;   // offset of the 8/16 byte header
    uint64_t size = 0;     // total box size including header
    uint64_t header = 0;   // header length (8 or 16)
    std::vector<Box> children;
    bool is_container() const { return !children.empty(); }
    uint64_t payload_offset() const { return offset + header; }
    uint64_t payload_size() const { return size - header; }
};

// Bounded big-endian reader over the whole file buffer.
class Cursor {
public:
    Cursor(const std::vector<uint8_t>& d, uint64_t pos, uint64_t end)
        : data(d), p(pos), limit(end) {}
    bool eof() const { return p >= limit; }
    uint64_t pos() const { return p; }
    uint64_t remaining() const { return p <= limit ? limit - p : 0; }
    const std::vector<uint8_t>& buffer() const { return data; }
    void skip(uint64_t n);
    void seek(uint64_t pos);
    uint8_t u8();
    uint16_t u16();
    uint32_t u32();
    uint64_t u64();
    FourCC fourcc();
    // Returns (version, flags) of a FullBox and advances past them.
    uint8_t fullbox(uint32_t& flags);
    std::vector<uint8_t> bytes(uint64_t n);

private:
    void need(uint64_t n) const;
    const std::vector<uint8_t>& data;
    uint64_t p;
    uint64_t limit;
};

struct BoxError : std::runtime_error {
    explicit BoxError(const std::string& m) : std::runtime_error(m) {}
};

struct FileError : std::runtime_error {
    explicit FileError(const std::string& m) : std::runtime_error(m) {}
};

struct ParseOptions {
    // 2 GiB per-file limit from the task constraints.
    uint64_t max_file_size = 2ULL * 1024 * 1024 * 1024;
};

// Reads a whole file, enforcing the size limit. Empty file -> BoxError.
std::vector<uint8_t> read_file(const std::string& path, const ParseOptions& opt);

// Parses every top-level box, recursing into known containers.
// Rejects truncated headers, out-of-range/overflowing lengths, size==0 inside
// containers, encrypted boxes and unknown critical boxes at structural depth.
std::vector<Box> parse_boxes(const std::vector<uint8_t>& data,
                             const ParseOptions& opt = {});

const Box* find_box(const std::vector<Box>& boxes, const char* type);
const Box* find_child(const Box& parent, const char* type);
std::vector<const Box*> find_all(const std::vector<Box>& boxes,
                                 const char* type);
std::vector<const Box*> find_children(const Box& parent, const char* type);

// Raw payload of an stsd box with the entry_count field removed, i.e. the
// concatenated sample entries used as the codec configuration fingerprint.
std::vector<uint8_t> stsd_entries_payload(
    const std::vector<uint8_t>& data, const Box& stsd);

// FIPS-180 SHA-256 of a byte buffer, rendered as lowercase hex.
std::string sha256_hex(const std::vector<uint8_t>& bytes);

}  // namespace mp4
