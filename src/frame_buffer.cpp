#include "frame_buffer.hpp"

#include <sys/stat.h>
#include <sys/types.h>
#include <unistd.h>

#include <cerrno>
#include <cstdio>
#include <cstring>
#include <filesystem>
#include <fstream>

#include "errors.hpp"

namespace tr {

FrameBuffer::FrameBuffer(int width, int height) : width_(width), height_(height) {
  if (width <= 0 || height <= 0 || width > kMaxDimension ||
      height > kMaxDimension) {
    throw UsageError("invalid frame size " + std::to_string(width) + "x" +
                     std::to_string(height) + ": each dimension must be in "
                     "1..4096");
  }
  // Multiply with the full-width type to prove the allocation cannot wrap.
  const std::size_t count =
      static_cast<std::size_t>(width) * static_cast<std::size_t>(height);
  const std::size_t bytes = count * 3u;
  if (count > kMaxPixels || bytes > kMaxFrameBytes) {
    throw UsageError("frame size exceeds pixel budget");
  }
  pixels_.resize(bytes);
}

void FrameBuffer::setPixel(int x, int y, std::uint8_t r, std::uint8_t g,
                           std::uint8_t b) {
  const std::size_t i = pixelIndex(x, y);
  pixels_[i] = r;
  pixels_[i + 1] = g;
  pixels_[i + 2] = b;
}

void FrameBuffer::getPixel(int x, int y, std::uint8_t* r, std::uint8_t* g,
                           std::uint8_t* b) const {
  const std::size_t i = pixelIndex(x, y);
  *r = pixels_[i];
  *g = pixels_[i + 1];
  *b = pixels_[i + 2];
}

void FrameBuffer::fill(std::uint8_t r, std::uint8_t g, std::uint8_t b) {
  for (std::size_t i = 0; i + 2 < pixels_.size(); i += 3) {
    pixels_[i] = r;
    pixels_[i + 1] = g;
    pixels_[i + 2] = b;
  }
}

void writePpmAtomic(const std::string& path, const FrameBuffer& fb) {
  const std::string tmp = path + ".tmp." + std::to_string(getpid());
  {
    std::ofstream out(tmp, std::ios::binary | std::ios::trunc);
    if (!out) {
      throw IoError("cannot open for write: " + path);
    }
    out << "P6\n" << fb.width() << ' ' << fb.height() << "\n255\n";
    if (!out) {
      std::remove(tmp.c_str());
      throw IoError("cannot write header: " + path);
    }
    out.write(reinterpret_cast<const char*>(fb.bytes().data()),
              static_cast<std::streamsize>(fb.bytes().size()));
    out.flush();
    if (!out) {
      std::remove(tmp.c_str());
      throw IoError("cannot write pixels: " + path);
    }
  }  // close before rename
  if (std::rename(tmp.c_str(), path.c_str()) != 0) {
    std::remove(tmp.c_str());
    throw IoError("cannot replace file atomically: " + path + " (" +
                  std::strerror(errno) + ")");
  }
}

namespace {

std::vector<std::uint8_t> readAllBytes(const std::string& path) {
  std::ifstream in(path, std::ios::binary);
  if (!in) throw IoError("cannot open for read: " + path);
  std::vector<std::uint8_t> data((std::istreambuf_iterator<char>(in)),
                                 std::istreambuf_iterator<char>());
  if (in.bad()) throw IoError("read failure: " + path);
  return data;
}

// Read one unsigned token, skipping whitespace and '#' comments.
bool ppmToken(const std::vector<std::uint8_t>& d, std::size_t& pos,
              int& value) {
  const auto skip = [&]() {
    while (pos < d.size()) {
      const char c = static_cast<char>(d[pos]);
      if (c == '#') {
        while (pos < d.size() && d[pos] != '\n') ++pos;
      } else if (c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
                 c == '\v' || c == '\f') {
        ++pos;
      } else {
        break;
      }
    }
  };
  skip();
  if (pos >= d.size() || d[pos] < '0' || d[pos] > '9') return false;
  long v = 0;
  while (pos < d.size() && d[pos] >= '0' && d[pos] <= '9') {
    v = v * 10 + (d[pos] - '0');
    if (v > 70000) return false;
    ++pos;
  }
  value = static_cast<int>(v);
  return true;
}

}  // namespace

FrameBuffer readPpm(const std::string& path) {
  const std::vector<std::uint8_t> d = readAllBytes(path);
  std::size_t pos = 0;
  if (d.size() < 2 || d[0] != 'P' || d[1] != '6') {
    throw IoError("not a P6 PPM: " + path);
  }
  pos = 2;
  int width = 0, height = 0, maxval = 0;
  if (!ppmToken(d, pos, width) || !ppmToken(d, pos, height) ||
      !ppmToken(d, pos, maxval)) {
    throw IoError("malformed PPM header: " + path);
  }
  if (maxval != 255) {
    throw IoError("unsupported maxval (only 255): " + path);
  }
  // Exactly one single whitespace byte separates header from raster.
  if (pos >= d.size()) throw IoError("truncated PPM: " + path);
  const char sep = static_cast<char>(d[pos++]);
  if (sep != ' ' && sep != '\t' && sep != '\n' && sep != '\r' &&
      sep != '\v' && sep != '\f') {
    throw IoError("missing raster separator: " + path);
  }
  try {
    FrameBuffer built(width, height);  // validates geometry
    const std::size_t need = built.bytes().size();
    if (d.size() - pos != need) {
      throw IoError("raster length mismatch: " + path);
    }
    std::memcpy(built.bytesWritable().data(), d.data() + pos, need);
    return built;
  } catch (const UsageError& e) {
    throw IoError(std::string("invalid PPM geometry: ") + e.what() + ": " +
                  path);
  }
}

void ensureOutputDir(const std::string& dir) {
  std::error_code ec;
  std::filesystem::create_directories(dir, ec);
  if (ec) throw IoError("cannot create output directory: " + dir + " (" +
                        ec.message() + ")");
}

void checkOutputDirSafe(const std::string& dir,
                        const std::string& manifestName) {
  std::error_code ec;
  if (!std::filesystem::exists(dir, ec)) return;  // will be created
  for (const auto& entry : std::filesystem::directory_iterator(dir, ec)) {
    const std::string name = entry.path().filename().string();
    const bool ppm = name.size() > 4 &&
                     name.compare(name.size() - 4, 4, ".ppm") == 0;
    if (!ppm && name != manifestName) {
      throw UsageError("refusing to write: non-PPM file already present: " +
                       entry.path().string());
    }
  }
  if (ec) throw IoError("cannot inspect output directory: " + dir);
}

}  // namespace tr
