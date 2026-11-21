#include "lut/image.hpp"

#include <algorithm>
#include <cctype>
#include <cmath>
#include <limits>
#include <sstream>

#include "lut/io_util.hpp"

namespace lut {
namespace {

// PPM tokenizer: whitespace and '#' comments, tracking the byte offset where
// the binary raster begins so the header/raster boundary is exact.
class HeaderParser {
public:
    HeaderParser(const std::string& data, const std::string& path)
        : data_(data), path_(path) {}

    std::string nextToken() {
        skipWhitespaceAndComments();
        const std::size_t start = pos_;
        while (pos_ < data_.size() && !std::isspace(static_cast<unsigned char>(
                                           data_[pos_])) &&
               data_[pos_] != '#') {
            ++pos_;
        }
        if (start == pos_) {
            throw IoError("malformed PPM header in " + path_);
        }
        return data_.substr(start, pos_ - start);
    }

    std::uint32_t nextUint32(const char* what) {
        const std::string token = nextToken();
        std::size_t consumed = 0;
        unsigned long long value = 0;
        try {
            value = std::stoull(token, &consumed);
        } catch (const std::exception&) {
            throw IoError(std::string("invalid PPM ") + what + " in " + path_);
        }
        if (consumed != token.size() ||
            value > std::numeric_limits<std::uint32_t>::max()) {
            throw IoError(std::string("PPM ") + what + " overflow in " +
                          path_);
        }
        return static_cast<std::uint32_t>(value);
    }

    // The PPM spec mandates one whitespace byte after maxval, but real files
    // use CRLF (two bytes) and occasionally a comment line there. Consume the
    // required separator and any additional header whitespace/comments so the
    // position lands exactly on the first raster byte.
    void enterRaster() {
        if (pos_ >= data_.size() ||
            !std::isspace(static_cast<unsigned char>(data_[pos_]))) {
            throw IoError("missing raster separator in PPM " + path_);
        }
        ++pos_;  // The single spec-required separator.
        for (;;) {
            while (pos_ < data_.size() &&
                   std::isspace(static_cast<unsigned char>(data_[pos_]))) {
                ++pos_;
            }
            if (pos_ < data_.size() && data_[pos_] == '#') {
                while (pos_ < data_.size() && data_[pos_] != '\n') {
                    ++pos_;
                }
                continue;
            }
            break;
        }
    }

    std::size_t rasterOffset() const { return pos_; }

private:
    void skipWhitespaceAndComments() {
        for (;;) {
            while (pos_ < data_.size() &&
                   std::isspace(static_cast<unsigned char>(data_[pos_]))) {
                ++pos_;
            }
            if (pos_ < data_.size() && data_[pos_] == '#') {
                while (pos_ < data_.size() && data_[pos_] != '\n') {
                    ++pos_;
                }
            } else {
                break;
            }
        }
    }

    const std::string& data_;
    const std::string& path_;
    std::size_t pos_ = 0;
};

}  // namespace

Image readPpm(const std::string& path) {
    const std::string data = readBinaryFile(path);
    HeaderParser hp(data, path);

    const std::string magic = hp.nextToken();
    if (magic != "P6") {
        throw IoError("only binary PPM (P6) is supported, got '" + magic +
                      "' in " + path);
    }
    Image image;
    image.width = hp.nextUint32("width");
    image.height = hp.nextUint32("height");
    image.maxval = hp.nextUint32("maxval");
    if (image.width == 0 || image.height == 0) {
        throw IoError("PPM dimensions must be non-zero in " + path);
    }
    if (image.maxval == 0 || image.maxval > 65535) {
        throw IoError("illegal PPM maxval in " + path);
    }
    hp.enterRaster();

    const std::size_t bytesPerSample = image.maxval < 256 ? 1 : 2;
    // Reject dimensions whose raster size would overflow size_t before any
    // multiplication or allocation, so a hostile header cannot wrap to zero.
    constexpr std::size_t kMaxSize = std::numeric_limits<std::size_t>::max();
    const std::size_t pixels = image.pixelCount();
    if (pixels > kMaxSize / 3 ||
        pixels * 3 > kMaxSize / bytesPerSample) {
        throw IoError("PPM dimensions overflow addressable size in " + path);
    }
    const std::size_t need = pixels * 3 * bytesPerSample;
    const std::size_t available = data.size() - hp.rasterOffset();
    if (available < need) {
        throw IoError("truncated PPM raster in " + path + ": need " +
                      std::to_string(need) + " bytes, have " +
                      std::to_string(available));
    }
    if (available > need) {
        throw IoError("trailing bytes after PPM raster in " + path);
    }

    image.pixels.resize(image.pixelCount());
    std::size_t offset = hp.rasterOffset();
    const double scale = static_cast<double>(image.maxval);
    for (RGB& pixel : image.pixels) {
        double channel[3];
        for (int c = 0; c < 3; ++c) {
            std::uint32_t raw = 0;
            if (bytesPerSample == 1) {
                raw = static_cast<unsigned char>(data[offset++]);
            } else {
                // 16-bit PPM samples are unsigned big-endian.
                raw = (static_cast<unsigned char>(data[offset]) << 8) |
                      static_cast<unsigned char>(data[offset + 1]);
                offset += 2;
            }
            channel[c] = raw / scale;
        }
        pixel = RGB{channel[0], channel[1], channel[2]};
    }
    return image;
}

std::string renderPpm(const Image& image) {
    if (image.maxval == 0 || image.maxval > 65535) {
        throw IoError("illegal PPM maxval for output");
    }
    std::ostringstream header;
    header << "P6\n" << image.width << ' ' << image.height << '\n'
           << image.maxval << '\n';
    std::string out = header.str();

    const bool wide = image.maxval >= 256;
    out.reserve(out.size() + image.pixelCount() * 3 * (wide ? 2 : 1));
    for (const RGB& pixel : image.pixels) {
        const double channels[3] = {pixel.r, pixel.g, pixel.b};
        for (double value : channels) {
            value = std::clamp(value, 0.0, 1.0);
            std::uint32_t raw = static_cast<std::uint32_t>(
                std::lround(value * image.maxval));
            raw = std::min(raw, image.maxval);
            if (wide) {
                out.push_back(static_cast<char>((raw >> 8) & 0xFF));
                out.push_back(static_cast<char>(raw & 0xFF));
            } else {
                out.push_back(static_cast<char>(raw & 0xFF));
            }
        }
    }
    return out;
}

void writePpm(const std::string& path, const Image& image) {
    writeFileAtomic(path, renderPpm(image));
}

}  // namespace lut
