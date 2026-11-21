#include "box_writer.hpp"

#include <limits>

namespace mp4flow {

void BoxWriter::reserve(size_t extra) {
  size_t cur = out_.size();
  if (extra > std::numeric_limits<size_t>::max() - cur)
    throw Mp4Error(ErrorKind::LengthOverflow, "<writer>",
                   "buffer size overflow");
  if (uint64_t(cur) + extra > kMaxFileSize)
    throw Mp4Error(ErrorKind::LengthOverflow, "<writer>",
                   "output exceeds file size limit");
  out_.reserve(cur + extra);
}

void BoxWriter::u8(uint8_t v) {
  reserve(1);
  out_.push_back(v);
}
void BoxWriter::u16(uint16_t v) {
  reserve(2);
  out_.push_back(uint8_t(v >> 8));
  out_.push_back(uint8_t(v));
}
void BoxWriter::u32(uint32_t v) {
  reserve(4);
  out_.push_back(uint8_t(v >> 24));
  out_.push_back(uint8_t(v >> 16));
  out_.push_back(uint8_t(v >> 8));
  out_.push_back(uint8_t(v));
}
void BoxWriter::u64(uint64_t v) {
  reserve(8);
  for (int shift = 56; shift >= 0; shift -= 8)
    out_.push_back(uint8_t(v >> shift));
}
void BoxWriter::bytes(const uint8_t* p, size_t n) {
  if (!n) return;
  reserve(n);
  out_.insert(out_.end(), p, p + n);
}
void BoxWriter::zero(size_t n) {
  reserve(n);
  out_.insert(out_.end(), n, uint8_t(0));
}

size_t BoxWriter::beginBox(uint32_t type) {
  size_t pos = out_.size();
  u32(0); // size placeholder, patched in endBox()
  u32(type);
  stack_.push_back(pos);
  return pos;
}

size_t BoxWriter::beginFull(uint32_t type, uint8_t version, uint32_t flags) {
  size_t pos = beginBox(type);
  u8(version);
  u8(uint8_t((flags >> 16) & 0xff));
  u8(uint8_t((flags >> 8) & 0xff));
  u8(uint8_t(flags & 0xff));
  return pos;
}

void BoxWriter::endBox() {
  if (stack_.empty())
    throw Mp4Error(ErrorKind::BadValue, "<writer>", "endBox without beginBox");
  size_t pos = stack_.back();
  stack_.pop_back();
  uint64_t total = out_.size() - pos;
  if (total > std::numeric_limits<uint32_t>::max())
    throw Mp4Error(ErrorKind::LengthOverflow, "<writer>",
                   "box larger than 32-bit size (64-bit not emitted)");
  uint32_t sz = uint32_t(total);
  out_[pos] = uint8_t(sz >> 24);
  out_[pos + 1] = uint8_t(sz >> 16);
  out_[pos + 2] = uint8_t(sz >> 8);
  out_[pos + 3] = uint8_t(sz);
}

} // namespace mp4flow
