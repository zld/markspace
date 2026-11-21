#include "box_reader.hpp"

#include <cstring>
#include <fstream>
#include <limits>
#include <sstream>

namespace mp4flow {

namespace {
bool isContainer(uint32_t t) {
  switch (t) {
  case fourcc("moov"):
  case fourcc("trak"):
  case fourcc("mdia"):
  case fourcc("minf"):
  case fourcc("stbl"):
  case fourcc("dinf"):
  case fourcc("edts"):
  case fourcc("mvex"):
  case fourcc("moof"):
  case fourcc("traf"):
  case fourcc("mfra"):
  case fourcc("sinf"):
  case fourcc("schi"):
    return true;
  default:
    return false;
  }
}
} // namespace

Mp4Error::Mp4Error(ErrorKind k, std::string boxPath, const std::string& msg)
    : std::runtime_error(msg), kind(k), path(std::move(boxPath)) {}

std::string typeName(uint32_t t) {
  std::string r(4, ' ');
  r[0] = char((t >> 24) & 0xff);
  r[1] = char((t >> 16) & 0xff);
  r[2] = char((t >> 8) & 0xff);
  r[3] = char(t & 0xff);
  return r;
}

const Box* Box::findChild(uint32_t t) const {
  for (const auto& c : children)
    if (c.type == t) return &c;
  return nullptr;
}

const Box* findPath(const Box& root, std::initializer_list<uint32_t> path) {
  const Box* cur = &root;
  for (uint32_t t : path) {
    cur = cur->findChild(t);
    if (!cur) return nullptr;
  }
  return cur;
}

const Box* findDeep(const Box& root, uint32_t t) {
  for (const auto& c : root.children) {
    if (c.type == t) return &c;
    if (const Box* d = findDeep(c, t)) return d;
  }
  return nullptr;
}

uint64_t checkedAdd(uint64_t a, uint64_t b, const std::string& path) {
  if (b > std::numeric_limits<uint64_t>::max() - a)
    throw Mp4Error(ErrorKind::LengthOverflow, path, "offset/size overflow");
  return a + b;
}

std::vector<Box> parseBoxes(const uint8_t* base, uint64_t start, uint64_t end,
                            bool topLevel) {
  std::vector<Box> out;
  uint64_t off = start;
  while (off < end) {
    std::string p = topLevel ? "<top>" : "<nested>";
    if (end - off < 8)
      throw Mp4Error(ErrorKind::Truncated, p, "box header truncated");
    uint32_t size32 = (uint32_t(base[off]) << 24) | (uint32_t(base[off + 1]) << 16) |
                      (uint32_t(base[off + 2]) << 8) | uint32_t(base[off + 3]);
    uint32_t type = (uint32_t(base[off + 4]) << 24) | (uint32_t(base[off + 5]) << 16) |
                    (uint32_t(base[off + 6]) << 8) | uint32_t(base[off + 7]);
    p = typeName(type);
    uint64_t hdr = 8;
    uint64_t total = 0;
    if (size32 == 1) { // 64-bit largesize
      if (end - off < 16)
        throw Mp4Error(ErrorKind::Truncated, p, "largesize header truncated");
      hdr = 16;
      Cursor c(base, end, off + 8, p);
      total = c.u64();
      if (total < 16)
        throw Mp4Error(ErrorKind::LengthOverflow, p, "largesize smaller than header");
    } else if (size32 == 0) { // box extends to end of enclosing range
      total = end - off;
    } else {
      total = size32;
      if (total < 8)
        throw Mp4Error(ErrorKind::LengthOverflow, p, "size smaller than header");
    }
    if (total > end - off)
      throw Mp4Error(ErrorKind::Truncated, p, "box extends past enclosing range");
    Box b;
    b.type = type;
    b.start = off;
    b.headerSize = hdr;
    b.size = total;

    // stsd is a full box whose payload starts with entry_count(4) then
    // sample entries (each its own nested box). Parse it as a special case.
    if (isContainer(type)) {
      b.children = parseBoxes(base, b.payloadOffset(), off + total, false);
    } else if (type == fourcc("stsd")) {
      if (b.payloadSize() < 4)
        throw Mp4Error(ErrorKind::Truncated, p, "stsd too short");
      Cursor c(base, off + total, b.payloadOffset(), p);
      readFull(c);
      c.u32(); // entry count
      b.children = parseBoxes(base, c.pos(), off + total, false);
    }
    out.push_back(std::move(b));
    off += total;
  }
  return out;
}

ParsedFile parseFile(const std::string& path) {
  std::ifstream f(path, std::ios::binary | std::ios::ate);
  if (!f)
    throw Mp4Error(ErrorKind::Io, path, "cannot open file");
  std::streamoff len = f.tellg();
  if (len < 0)
    throw Mp4Error(ErrorKind::Io, path, "cannot stat file");
  uint64_t ulen = uint64_t(len);
  if (ulen > kMaxFileSize) {
    std::ostringstream os;
    os << "file " << ulen << " bytes exceeds limit " << kMaxFileSize;
    throw Mp4Error(ErrorKind::LengthOverflow, path, os.str());
  }
  ParsedFile pf;
  pf.path = path;
  pf.data.resize(size_t(ulen));
  f.seekg(0);
  if (ulen && !f.read(reinterpret_cast<char*>(pf.data.data()),
                      std::streamsize(ulen)))
    throw Mp4Error(ErrorKind::Io, path, "short read");
  pf.boxes = parseBoxes(pf.data.data(), 0, ulen, true);
  return pf;
}

Cursor::Cursor(const uint8_t* base, uint64_t cap, uint64_t off, std::string p)
    : path(std::move(p)), base_(base), cap_(cap), off_(off) {}

void Cursor::require(uint64_t n) const {
  if (n > cap_ - off_)
    throw Mp4Error(ErrorKind::Truncated, path, "field extends past box");
}

uint8_t Cursor::u8() {
  require(1);
  return base_[off_++];
}
uint16_t Cursor::u16() {
  require(2);
  uint16_t v = uint16_t((uint16_t(base_[off_]) << 8) | base_[off_ + 1]);
  off_ += 2;
  return v;
}
uint32_t Cursor::u32() {
  require(4);
  uint32_t v = 0;
  for (uint32_t i = 0; i < 4; ++i)
    v = (v << 8) | uint32_t(base_[off_ + i]);
  off_ += 4;
  return v;
}
uint64_t Cursor::u64() {
  require(8);
  uint64_t v = 0;
  for (uint32_t i = 0; i < 8; ++i)
    v = (v << 8) | uint64_t(base_[off_ + i]);
  off_ += 8;
  return v;
}
int32_t Cursor::i32() { return int32_t(u32()); }
int64_t Cursor::i64() { return int64_t(u64()); }

void Cursor::skip(uint64_t n) {
  require(n);
  off_ += n;
}
const uint8_t* Cursor::take(uint64_t n) {
  require(n);
  const uint8_t* p = base_ + off_;
  off_ += n;
  return p;
}

FullHeader readFull(Cursor& c) {
  uint8_t ver = c.u8();
  uint32_t f = uint32_t(c.u8()) << 16 | uint32_t(c.u8()) << 8 | c.u8();
  return {ver, f};
}

} // namespace mp4flow
