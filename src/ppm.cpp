#include "ppm.hpp"

#include <cstdio>
#include <string>
#include <vector>

#include "errors.hpp"

namespace tl {

void write_ppm_atomic(const std::string& path, const FrameBuffer& fb) {
  const std::string tmp = path + ".tmp";
  FILE* f = std::fopen(tmp.c_str(), "wb");
  if (!f) throw IoError("cannot open temp file '" + tmp + "' for writing");

  char header[64];
  const int hn = std::snprintf(header, sizeof(header), "P6\n%d %d\n255\n",
                               fb.width(), fb.height());
  bool ok = hn > 0 && std::fwrite(header, 1, static_cast<size_t>(hn), f) ==
                          static_cast<size_t>(hn);
  ok = ok && std::fwrite(fb.pixels().data(), 1, fb.pixels().size(), f) ==
                 fb.pixels().size();
  if (std::fclose(f) != 0) ok = false;
  if (!ok) {
    std::remove(tmp.c_str());
    throw IoError("failed writing temp file '" + tmp + "'");
  }
  if (std::rename(tmp.c_str(), path.c_str()) != 0) {
    std::remove(tmp.c_str());
    throw IoError("cannot rename '" + tmp + "' -> '" + path + "'");
  }
}

namespace {

std::vector<uint8_t> read_all(const std::string& path) {
  FILE* f = std::fopen(path.c_str(), "rb");
  if (!f) throw IoError("cannot open '" + path + "' for reading");
  std::vector<uint8_t> data;
  uint8_t buf[65536];
  size_t n;
  while ((n = std::fread(buf, 1, sizeof(buf), f)) > 0)
    data.insert(data.end(), buf, buf + n);
  const bool bad = std::ferror(f) != 0;
  std::fclose(f);
  if (bad) throw IoError("read error on '" + path + "'");
  return data;
}

bool is_space(uint8_t c) {
  return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\v' || c == '\f';
}

// Parses one whitespace/comment-delimited token starting at *pos.
std::string next_token(const std::vector<uint8_t>& d, size_t* pos,
                       const std::string& path) {
  for (;;) {
    if (*pos >= d.size())
      throw DataError("P6 '" + path + "': unexpected end of header");
    if (is_space(d[*pos])) {
      ++*pos;
    } else if (d[*pos] == '#') {
      while (*pos < d.size() && d[*pos] != '\n') ++*pos;
    } else {
      break;
    }
  }
  const size_t begin = *pos;
  while (*pos < d.size() && !is_space(d[*pos]) && d[*pos] != '#') ++*pos;
  return std::string(reinterpret_cast<const char*>(d.data() + begin), *pos - begin);
}

long parse_decimal(const std::string& tok, const char* what,
                   const std::string& path) {
  if (tok.empty() || tok.size() > 7)
    throw DataError("P6 '" + path + "': invalid " + what + " '" + tok + "'");
  long v = 0;
  for (char c : tok) {
    if (c < '0' || c > '9')
      throw DataError("P6 '" + path + "': invalid " + what + " '" + tok + "'");
    v = v * 10 + (c - '0');
  }
  return v;
}

void check_file_dimensions(long w, long h, const std::string& path) {
  if (w <= 0 || h <= 0 || w > kMaxDimension || h > kMaxDimension ||
      w * h > kMaxPixelsPerFrame) {
  throw DataError("P6 '" + path + "': dimensions " + std::to_string(w) + "x" +
                  std::to_string(h) + " outside allowed range");
  }
}

}  // namespace

FrameBuffer read_ppm_strict(const std::string& path) {
  const std::vector<uint8_t> data = read_all(path);
  size_t pos = 0;
  if (next_token(data, &pos, path) != "P6")
    throw DataError("P6 '" + path + "': bad magic, expected P6");
  const long w = parse_decimal(next_token(data, &pos, path), "width", path);
  const long h = parse_decimal(next_token(data, &pos, path), "height", path);
  check_file_dimensions(w, h, path);
  if (parse_decimal(next_token(data, &pos, path), "maxval", path) != 255)
    throw DataError("P6 '" + path + "': maxval must be 255");
  // Exactly one whitespace byte separates the maxval token from the payload.
  if (pos >= data.size() || !is_space(data[pos]))
    throw DataError("P6 '" + path + "': missing payload separator");
  ++pos;
  const size_t expected = static_cast<size_t>(w) * h * 3;
  if (data.size() - pos < expected)
    throw DataError("P6 '" + path + "': truncated payload, need " +
                    std::to_string(expected) + " bytes");
  if (data.size() - pos > expected)
    throw DataError("P6 '" + path + "': trailing bytes after payload");
  FrameBuffer fb(static_cast<int>(w), static_cast<int>(h));
  std::copy(data.begin() + static_cast<long>(pos), data.end(),
            fb.pixels().begin());
  return fb;
}

}  // namespace tl
