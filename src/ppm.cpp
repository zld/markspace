#include "ppm.hpp"

#include <cerrno>
#include <charconv>
#include <cstdio>
#include <fstream>
#include <sstream>

#include "errors.hpp"

namespace lutcheck {
namespace {

struct Header {
  char magic[2] = {'P', '6'};
  std::uint32_t width = 0;
  std::uint32_t height = 0;
  int max_value = 0;
  std::size_t data_offset = 0;
};

// Advances past whitespace and comments, mirroring the PPM spec where a '#'
// starts a comment that runs to the end of the line.
void skip_space(const std::string& text, std::size_t* pos) {
  while (*pos < text.size()) {
    const char c = text[*pos];
    if (c == '#') {
      while (*pos < text.size() && text[*pos] != '\n') ++(*pos);
    } else if (c == ' ' || c == '\t' || c == '\r' || c == '\n' ||
               c == '\v' || c == '\f') {
      ++(*pos);
    } else {
      return;
    }
  }
}

std::uint32_t parse_dimension(const std::string& text, std::size_t* pos,
                              const std::string& path, const char* what) {
  skip_space(text, pos);
  const std::size_t start = *pos;
  while (*pos < text.size() && text[*pos] >= '0' && text[*pos] <= '9') ++(*pos);
  if (start == *pos) {
    fail(ExitCode::Input, path + ": expected " + what + " in PPM header");
  }
  unsigned long long value = 0;
  const auto result =
      std::from_chars(text.data() + start, text.data() + *pos, value);
  if (result.ec != std::errc()) {
    fail(ExitCode::Input, path + ": " + what + " does not fit in range");
  }
  if (value == 0 || value > 0xFFFFFFFFull) {
    fail(ExitCode::Input, path + ": invalid " + what + " " +
                              std::to_string(value));
  }
  return static_cast<std::uint32_t>(value);
}

std::size_t checked_pixel_count(std::uint32_t width, std::uint32_t height,
                                std::size_t max_pixels,
                                const std::string& path) {
  // 64-bit math: width*height can otherwise overflow for hostile headers.
  const auto wide = static_cast<unsigned long long>(width) *
                    static_cast<unsigned long long>(height);
  if (wide > max_pixels) {
    fail(ExitCode::Input, path + ": image has " + std::to_string(wide) +
                              " pixels, limit is " +
                              std::to_string(max_pixels));
  }
  return static_cast<std::size_t>(wide);
}

std::uint8_t clamp_byte(double value) {
  if (!(value > 0.0)) return 0;  // also catches NaN
  if (value >= 1.0) return 255;
  return static_cast<std::uint8_t>(value * 255.0 + 0.5);
}

}  // namespace

Image read_ppm(const std::string& path) {
  std::ifstream stream(path, std::ios::binary);
  if (!stream) {
    fail(ExitCode::Input, path + ": cannot open image");
  }
  std::ostringstream buffer;
  buffer << stream.rdbuf();
  if (stream.bad()) {
    fail(ExitCode::Input, path + ": read failure");
  }
  const std::string text = buffer.str();
  if (text.size() < 2 || text[0] != 'P' || (text[1] != '6' && text[1] != '3')) {
    fail(ExitCode::Input, path + ": not a PPM file (expected P6 or P3)");
  }
  const bool binary = text[1] == '6';

  std::size_t pos = 2;
  const std::uint32_t width = parse_dimension(text, &pos, path, "width");
  const std::uint32_t height = parse_dimension(text, &pos, path, "height");
  const std::uint32_t max_value = parse_dimension(text, &pos, path, "max value");
  if (max_value > 65535) {
    fail(ExitCode::Input, path + ": max value " + std::to_string(max_value) +
                              " exceeds 65535");
  }
  // Exactly one whitespace character terminates the header.
  if (pos >= text.size()) {
    fail(ExitCode::Input, path + ": truncated PPM header");
  }
  ++pos;

  Image image;
  image.width = width;
  image.height = height;
  const std::size_t count = checked_pixel_count(width, height, kMaxPixels, path);
  image.pixels.resize(count);
  const double scale = 1.0 / static_cast<double>(max_value);

  if (binary) {
    const std::size_t sample_bytes = max_value > 255 ? 2 : 1;
    const std::size_t expected = count * 3 * sample_bytes;
    if (text.size() - pos < expected) {
      fail(ExitCode::Input, path + ": truncated pixel data: expected " +
                                std::to_string(expected) + " bytes, found " +
                                std::to_string(text.size() - pos));
    }
    const unsigned char* data =
        reinterpret_cast<const unsigned char*>(text.data() + pos);
    for (std::size_t i = 0; i < count; ++i) {
      const std::size_t base = i * 3 * sample_bytes;
      for (std::size_t channel = 0; channel < 3; ++channel) {
        std::uint32_t raw = 0;
        if (sample_bytes == 1) {
          raw = data[base + channel];
        } else {
          raw = (static_cast<std::uint32_t>(data[base + channel * 2]) << 8) |
                data[base + channel * 2 + 1];
        }
        image.pixels[i][channel] = static_cast<double>(raw) * scale;
      }
    }
  } else {
    std::size_t cursor = pos;
    for (std::size_t i = 0; i < count; ++i) {
      for (std::size_t channel = 0; channel < 3; ++channel) {
        skip_space(text, &cursor);
        const std::size_t start = cursor;
        while (cursor < text.size() && text[cursor] >= '0' &&
               text[cursor] <= '9') {
          ++cursor;
        }
        if (start == cursor) {
          fail(ExitCode::Input, path + ": truncated ASCII pixel " +
                                    std::to_string(i));
        }
        unsigned long long raw = 0;
        const auto result = std::from_chars(text.data() + start,
                                            text.data() + cursor, raw);
        if (result.ec != std::errc() || raw > max_value) {
          fail(ExitCode::Input, path + ": invalid ASCII sample at pixel " +
                                    std::to_string(i));
        }
        image.pixels[i][channel] = static_cast<double>(raw) * scale;
      }
    }
  }
  return image;
}

std::string encode_ppm(const Image& image) {
  std::string out;
  out.reserve(static_cast<std::size_t>(image.width) * image.height * 3 + 32);
  out += "P6\n";
  out += std::to_string(image.width) + " " + std::to_string(image.height) +
         "\n255\n";
  for (const Vec3& pixel : image.pixels) {
    out.push_back(static_cast<char>(clamp_byte(pixel.r)));
    out.push_back(static_cast<char>(clamp_byte(pixel.g)));
    out.push_back(static_cast<char>(clamp_byte(pixel.b)));
  }
  return out;
}

void write_ppm_atomic(const std::string& path, const Image& image,
                      std::size_t max_pixels) {
  if (!(static_cast<unsigned long long>(image.width) * image.height <=
        max_pixels)) {
    fail(ExitCode::Output, path + ": refusing to write oversized image");
  }
  const std::string temp = path + ".tmp";
  const std::string payload = encode_ppm(image);
  {
    std::ofstream out(temp, std::ios::binary | std::ios::trunc);
    if (!out) {
      fail(ExitCode::Output, path + ": cannot open temporary output");
    }
    out.write(payload.data(), static_cast<std::streamsize>(payload.size()));
    out.flush();
    if (!out) {
      std::remove(temp.c_str());
      fail(ExitCode::Output, path + ": write failure");
    }
  }
  if (std::rename(temp.c_str(), path.c_str()) != 0) {
    std::remove(temp.c_str());
    fail(ExitCode::Output, path + ": cannot replace output file");
  }
}

}  // namespace lutcheck
