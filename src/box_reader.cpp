#include "fmp4/box_reader.hpp"

#include <cstring>

namespace fmp4 {

std::string Fmp4Error::make(const std::string &msg, const std::string &f,
                            uint64_t off) {
  if (f.empty()) return msg;
  return msg + " (" + f + " @0x" + [](uint64_t v) {
    static const char *h = "0123456789abcdef";
    std::string s;
    if (!v) return std::string("0");
    while (v) { s.push_back(h[v & 0xf]); v >>= 4; }
    return std::string(s.rbegin(), s.rend());
  }(off) + ")";
}

FileSource::FileSource(const std::string &path) : path_(path) {
  fp_ = std::fopen(path.c_str(), "rb");
  if (!fp_) throw Fmp4Error("cannot open file: " + path, path, 0);
  if (std::fseek(fp_, 0, SEEK_END) != 0)
    throw Fmp4Error("seek failed", path, 0);
  long sz = std::ftell(fp_);
  if (sz < 0) throw Fmp4Error("ftell failed", path, 0);
  size_ = uint64_t(sz);
}

FileSource::~FileSource() {
  if (fp_) std::fclose(fp_);
}

void FileSource::read(uint64_t off, void *dst, size_t n) {
  // fread is serialized by a seek per call; adequate for bounded metadata I/O.
  if (std::fseek(fp_, long(off), SEEK_SET) != 0)
    throw Fmp4Error("seek failed", path_, off);
  size_t got = std::fread(dst, 1, n, fp_);
  if (got != n) throw Fmp4Error("short read", path_, off);
}

MemorySource::MemorySource(std::vector<uint8_t> data, std::string name)
    : data_(std::move(data)), name_(std::move(name)) {}

void MemorySource::read(uint64_t off, void *dst, size_t n) {
  if (off > data_.size() || n > data_.size() - off)
    throw Fmp4Error("truncated input", name_, off);
  std::memcpy(dst, data_.data() + off, n);
}

BoxReader::BoxReader(IDataSource &src, uint64_t start, uint64_t end,
                     ParseLimits lim)
    : src_(src), start_(start), end_(end), cursor_(start),
      limits_(lim) {
  if (start > end || end > src.size())
    throw Fmp4Error("reader window out of bounds", src.name(), start);
}

void BoxReader::require(uint64_t off, size_t n) const {
  if (off > end_ || n > end_ - off)
    throw Fmp4Error("truncated box or parent boundary exceeded", src_.name(),
                    off);
}

bool BoxReader::next(BoxHeader &out) {
  if (cursor_ == end_) return false;
  if (cursor_ + 8 > end_)
    throw Fmp4Error("truncated box header", src_.name(), cursor_);
  if (++box_count_ > limits_.max_box_count)
    throw Fmp4Error("box count limit exceeded", src_.name(), cursor_);
  uint8_t hdr[16];
  src_.read(cursor_, hdr, 8);
  uint32_t size32 = (uint32_t(hdr[0]) << 24) | (uint32_t(hdr[1]) << 16) |
                    (uint32_t(hdr[2]) << 8) | uint32_t(hdr[3]);
  uint32_t type = (uint32_t(hdr[4]) << 24) | (uint32_t(hdr[5]) << 16) |
                  (uint32_t(hdr[6]) << 8) | uint32_t(hdr[7]);
  uint64_t hsize = 8, size;
  if (size32 == 1) { // 64-bit extended size
    hsize = 16;
    if (cursor_ + 16 > end_)
      throw Fmp4Error("truncated extended box header", src_.name(), cursor_);
    src_.read(cursor_ + 8, hdr, 8);
    size = 0;
    for (int i = 0; i < 8; ++i) size = (size << 8) | hdr[i];
    if (size < 16) throw Fmp4Error("invalid extended box size",
                                   src_.name(), cursor_);
  } else if (size32 == 0) { // box extends to end of window
    size = end_ - cursor_;
  } else {
    size = size32;
    if (size < 8) throw Fmp4Error("invalid box size", src_.name(), cursor_);
  }
  if (size > limits_.max_box_size)
    throw Fmp4Error("box exceeds size limit", src_.name(), cursor_);
  if (size > end_ - cursor_)
    throw Fmp4Error("box extends past parent boundary (corrupt length)",
                    src_.name(), cursor_);
  out.type = type;
  out.offset = cursor_;
  out.header_size = hsize;
  out.payload_offset = cursor_ + hsize;
  out.payload_size = size - hsize;
  cursor_ += size; // unknown boxes skipped purely by offset arithmetic
  return true;
}

BoxReader BoxReader::enter(const BoxHeader &parent, uint32_t depth) const {
  if (depth > limits_.max_depth)
    throw Fmp4Error("container nesting too deep", src_.name(), parent.offset);
  BoxReader r(src_, parent.payload_offset, parent.end(), limits_);
  r.depth_ = depth;
  r.box_count_ = 0;
  return r;
}

void BoxReader::read_abs(uint64_t off, void *dst, size_t n) const {
  require(off, n);
  src_.read(off, dst, n);
}

std::vector<uint8_t> BoxReader::read_payload(const BoxHeader &b,
                                             size_t cap) const {
  if (cap == 0) cap = static_cast<size_t>(limits_.max_total_meta);
  if (b.payload_size > cap)
    throw Fmp4Error("payload too large for metadata buffer", src_.name(),
                    b.payload_offset);
  std::vector<uint8_t> buf(static_cast<size_t>(b.payload_size));
  if (!buf.empty()) src_.read(b.payload_offset, buf.data(), buf.size());
  return buf;
}

uint8_t BoxReader::read_u8(const BoxHeader &b, uint64_t rel) const {
  uint8_t v;
  read_abs(b.payload_offset + rel, &v, 1);
  return v;
}

uint16_t BoxReader::read_u16(const BoxHeader &b, uint64_t rel) const {
  uint8_t v[2];
  read_abs(b.payload_offset + rel, v, 2);
  return uint16_t((uint16_t(v[0]) << 8) | v[1]);
}

uint32_t BoxReader::read_u32(const BoxHeader &b, uint64_t rel) const {
  uint8_t v[4];
  read_abs(b.payload_offset + rel, v, 4);
  return (uint32_t(v[0]) << 24) | (uint32_t(v[1]) << 16) |
         (uint32_t(v[2]) << 8) | uint32_t(v[3]);
}

uint64_t BoxReader::read_u64(const BoxHeader &b, uint64_t rel) const {
  uint8_t v[8];
  read_abs(b.payload_offset + rel, v, 8);
  uint64_t r = 0;
  for (uint8_t x : v) r = (r << 8) | x;
  return r;
}

} // namespace fmp4
