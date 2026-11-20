#include "grade/image_ppm.hpp"

#include <array>
#include <cerrno>
#include <cstdio>
#include <cstring>
#include <fstream>
#include <limits>
#include <sstream>
#include <unistd.h>

#include "grade/error.hpp"

namespace grade {

namespace {

// Cursor-based tokenizer for the whitespace-separated PPM header.
class HeaderReader {
public:
    HeaderReader(const uint8_t* data, size_t size)
        : data_(data), size_(size) {}

    // Reads the next non-comment token. Comments may only start where a
    // token is legal (after whitespace); an embedded '#' is left intact.
    std::string nextToken() {
        skipWhitespaceAndComments();
        if (pos_ >= size_) {
            throw FormatError("PPM header is truncated");
        }
        size_t start = pos_;
        while (pos_ < size_ && !isWhitespace(data_[pos_])) {
            ++pos_;
        }
        return std::string(reinterpret_cast<const char*>(data_ + start),
                           pos_ - start);
    }

    // Exactly one whitespace byte separates the header from binary data.
    void consumeSingleWhitespace() {
        if (pos_ >= size_ || !isWhitespace(data_[pos_])) {
            throw FormatError("PPM header missing whitespace before raster");
        }
        ++pos_;
    }

    size_t position() const noexcept { return pos_; }

private:
    static bool isWhitespace(uint8_t byte) {
        return byte == ' ' || byte == '\t' || byte == '\n' ||
               byte == '\r' || byte == 0x0b || byte == 0x0c;
    }

    void skipWhitespaceAndComments() {
        while (pos_ < size_) {
            if (isWhitespace(data_[pos_])) {
                ++pos_;
            } else if (data_[pos_] == '#') {
                while (pos_ < size_ && data_[pos_] != '\n' &&
                       data_[pos_] != '\r') {
                    ++pos_;
                }
            } else {
                break;
            }
        }
    }

    const uint8_t* data_;
    size_t size_;
    size_t pos_ = 0;
};

int parseDimension(const std::string& token, const char* field) {
    try {
        size_t consumed = 0;
        long value = std::stol(token, &consumed, 10);
        if (consumed != token.size() || value <= 0 ||
            value > kMaxImageDimension) {
            throw FormatError(std::string("PPM ") + field +
                              " must be a positive integer <= " +
                              std::to_string(kMaxImageDimension) + ", got '" +
                              token + "'");
        }
        return static_cast<int>(value);
    } catch (const std::invalid_argument&) {
        throw FormatError(std::string("PPM ") + field +
                          " is not an integer: '" + token + "'");
    } catch (const std::out_of_range&) {
        throw FormatError(std::string("PPM ") + field +
                          " is out of range: '" + token + "'");
    }
}

int parseMaxval(const std::string& token) {
    try {
        size_t consumed = 0;
        long value = std::stol(token, &consumed, 10);
        if (consumed != token.size() || value <= 0 ||
            value > kMaxPpmMaxval) {
            throw FormatError("PPM maxval must be in [1, 65535], got '" +
                              token + "'");
        }
        return static_cast<int>(value);
    } catch (const std::invalid_argument&) {
        throw FormatError("PPM maxval is not an integer: '" + token + "'");
    } catch (const std::out_of_range&) {
        throw FormatError("PPM maxval is out of range: '" + token + "'");
    }
}

uint16_t readSample(const uint8_t* data, bool sixteenBit) {
    if (!sixteenBit) {
        return data[0];
    }
    // PPM multi-byte samples are always most-significant byte first.
    return static_cast<uint16_t>((static_cast<uint16_t>(data[0]) << 8) |
                                 static_cast<uint16_t>(data[1]));
}

std::vector<uint8_t> readWholeFile(const std::string& path) {
    std::ifstream in(path, std::ios::binary);
    if (!in) {
        throw IoError("cannot open input image: " + path);
    }
    std::vector<uint8_t> bytes((std::istreambuf_iterator<char>(in)),
                               std::istreambuf_iterator<char>());
    if (in.bad()) {
        throw IoError("failure while reading input image: " + path);
    }
    return bytes;
}

} // namespace

PpmImage::PpmImage(int width, int height)
    : width_(width), height_(height) {
    if (width <= 0 || height <= 0 || width > kMaxImageDimension ||
        height > kMaxImageDimension) {
        throw UsageError("image dimensions out of supported range");
    }
    pixels_.resize(static_cast<size_t>(width) * static_cast<size_t>(height));
}

PpmImage parsePpmBytes(const std::vector<uint8_t>& bytes) {
    HeaderReader reader(bytes.data(), bytes.size());
    std::string magic = reader.nextToken();
    if (magic != "P6") {
        throw FormatError("only binary P6 PPM is supported, got magic '" +
                          magic + "'");
    }
    int width = parseDimension(reader.nextToken(), "width");
    int height = parseDimension(reader.nextToken(), "height");
    int maxval = parseMaxval(reader.nextToken());
    reader.consumeSingleWhitespace();

    PpmImage image(width, height);
    image.maxval_ = maxval;
    bool sixteenBit = maxval > 255;
    size_t sampleBytes = sixteenBit ? 2U : 1U;
    size_t totalSamples = image.pixelCount() *
                          static_cast<size_t>(kPpmChannels);
    if (totalSamples >
        std::numeric_limits<size_t>::max() / sampleBytes) {
        throw FormatError("PPM raster size overflows size_t");
    }
    size_t rasterBytes = totalSamples * sampleBytes;
    size_t start = reader.position();
    if (bytes.size() - start < rasterBytes) {
        throw FormatError("PPM raster is truncated: need " +
                          std::to_string(rasterBytes) + " bytes, found " +
                          std::to_string(bytes.size() - start));
    }
    double scale = 1.0 / static_cast<double>(maxval);
    const uint8_t* raster = bytes.data() + start;
    for (size_t pixel = 0; pixel < image.pixelCount(); ++pixel) {
        for (int channel = 0; channel < kPpmChannels; ++channel) {
            size_t offset = (pixel * kPpmChannels +
                             static_cast<size_t>(channel)) * sampleBytes;
            image.pixels_[pixel][static_cast<size_t>(channel)] =
                static_cast<double>(readSample(raster + offset, sixteenBit)) *
                scale;
        }
    }
    return image;
}

PpmImage readPpmFile(const std::string& path) {
    return parsePpmBytes(readWholeFile(path));
}

namespace {

// Encodes one normalized sample with rounding, clamped to [0, maxval].
uint16_t encodeSample(double normalized, int maxval) {
    if (normalized < 0.0) {
        normalized = 0.0;
    }
    if (normalized > 1.0) {
        normalized = 1.0;
    }
    double scaled = normalized * static_cast<double>(maxval) + 0.5;
    return static_cast<uint16_t>(scaled);
}

void flushOrThrow(FILE* file, const std::string& path) {
    if (std::fflush(file) != 0) {
        throw IoError("flush failed for " + path + ": " +
                      std::strerror(errno));
    }
    int fd = fileno(file);
    if (fd >= 0 && fsync(fd) != 0) {
        throw IoError("fsync failed for " + path + ": " +
                      std::strerror(errno));
    }
}

} // namespace

void writePpmFileAtomic(const std::string& path, const PpmImage& image) {
    std::string tempPath = path + ".tmp." + std::to_string(::getpid());
    FILE* file = std::fopen(tempPath.c_str(), "wb");
    if (file == nullptr) {
        throw IoError("cannot create temporary file " + tempPath + ": " +
                      std::strerror(errno));
    }
    try {
        std::ostringstream header;
        header << "P6\n# written by lutgrade\n" << image.width() << ' '
               << image.height() << '\n' << image.maxval() << '\n';
        std::string headerText = header.str();
        if (std::fwrite(headerText.data(), 1, headerText.size(), file) !=
            headerText.size()) {
            throw IoError("header write failed for " + tempPath);
        }
        bool sixteenBit = image.maxval() > 255;
        for (const auto& pixel : image.pixels()) {
            for (int channel = 0; channel < kPpmChannels; ++channel) {
                uint16_t sample =
                    encodeSample(pixel[static_cast<size_t>(channel)],
                                 image.maxval());
                if (sixteenBit) {
                    uint8_t pair[2] = {
                        static_cast<uint8_t>(sample >> 8),
                        static_cast<uint8_t>(sample & 0xFF)};
                    if (std::fwrite(pair, 1, 2, file) != 2) {
                        throw IoError("raster write failed for " + tempPath);
                    }
                } else {
                    uint8_t byte = static_cast<uint8_t>(sample);
                    if (std::fwrite(&byte, 1, 1, file) != 1) {
                        throw IoError("raster write failed for " + tempPath);
                    }
                }
            }
        }
        flushOrThrow(file, tempPath);
        if (std::fclose(file) != 0) {
            throw IoError("close failed for " + tempPath + ": " +
                          std::strerror(errno));
        }
        if (std::rename(tempPath.c_str(), path.c_str()) != 0) {
            throw IoError("atomic rename to " + path + " failed: " +
                          std::strerror(errno));
        }
    } catch (...) {
        std::fclose(file);
        std::remove(tempPath.c_str());
        throw;
    }
}

} // namespace grade
