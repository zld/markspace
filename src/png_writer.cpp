#include "renderer.hpp"

#include <zlib.h>

#include <cstdio>
#include <cstring>

namespace asspipe {

namespace {

void put_u32_be(std::vector<std::uint8_t>& v, std::uint32_t n) {
    v.push_back(static_cast<std::uint8_t>(n >> 24));
    v.push_back(static_cast<std::uint8_t>(n >> 16));
    v.push_back(static_cast<std::uint8_t>(n >> 8));
    v.push_back(static_cast<std::uint8_t>(n));
}

// CRC-32 per the PNG polynomial; computed directly to avoid sharing zlib's
// process-wide crc32 table assumptions (table built locally).
std::uint32_t png_crc32(const std::uint8_t* data, std::size_t len) {
    static std::uint32_t table[256];
    static bool ready = false;
    if (!ready) {
        for (std::uint32_t n = 0; n < 256; ++n) {
            std::uint32_t c = n;
            for (int k = 0; k < 8; ++k)
                c = (c & 1) ? (0xEDB88320u ^ (c >> 1)) : (c >> 1);
            table[n] = c;
        }
        ready = true;
    }
    std::uint32_t crc = 0xFFFFFFFFu;
    for (std::size_t i = 0; i < len; ++i)
        crc = table[(crc ^ data[i]) & 0xFFu] ^ (crc >> 8);
    return crc ^ 0xFFFFFFFFu;
}

void write_chunk(std::vector<std::uint8_t>& png, const char type[4],
                 const std::vector<std::uint8_t>& body) {
    put_u32_be(png, static_cast<std::uint32_t>(body.size()));
    std::size_t head = png.size();
    png.insert(png.end(), reinterpret_cast<const std::uint8_t*>(type),
               reinterpret_cast<const std::uint8_t*>(type) + 4);
    png.insert(png.end(), body.begin(), body.end());
    std::uint32_t crc = png_crc32(png.data() + head, 4 + body.size());
    put_u32_be(png, crc);
}

} // namespace

bool encode_png(const std::vector<std::uint8_t>& rgba, int width,
                int height, std::vector<std::uint8_t>& out_png) {
    if (width <= 0 || height <= 0) return false;
    const std::size_t row_bytes =
        static_cast<std::size_t>(width) * 4 + 1; // filter byte per row
    std::vector<std::uint8_t> raw(row_bytes * static_cast<std::size_t>(height));
    for (int y = 0; y < height; ++y) {
        std::size_t row = static_cast<std::size_t>(y) * row_bytes;
        raw[row] = 0; // filter type None: deterministic and simple
        std::memcpy(raw.data() + row + 1,
                    rgba.data() + static_cast<std::size_t>(y) * width * 4,
                    static_cast<std::size_t>(width) * 4);
    }

    uLongf bound = compressBound(static_cast<uLong>(raw.size()));
    std::vector<std::uint8_t> compressed(bound);
    int zrc = compress2(compressed.data(), &bound, raw.data(),
                        static_cast<uLong>(raw.size()), Z_DEFAULT_COMPRESSION);
    if (zrc != Z_OK) return false;
    compressed.resize(bound);

    out_png.clear();
    static const std::uint8_t kSignature[8] = {
        0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A};
    out_png.insert(out_png.end(), kSignature, kSignature + 8);

    std::vector<std::uint8_t> ihdr;
    put_u32_be(ihdr, static_cast<std::uint32_t>(width));
    put_u32_be(ihdr, static_cast<std::uint32_t>(height));
    ihdr.push_back(8); // bit depth
    ihdr.push_back(6); // colour type RGBA
    ihdr.push_back(0); // compression
    ihdr.push_back(0); // filter
    ihdr.push_back(0); // interlace
    write_chunk(out_png, "IHDR", ihdr);
    write_chunk(out_png, "IDAT", compressed);
    std::vector<std::uint8_t> empty;
    write_chunk(out_png, "IEND", empty);
    return true;
}

std::string event_png_path(const std::string& output_dir, int n) {
    char name[32];
    std::snprintf(name, sizeof(name), "/event_%04d.png", n);
    return output_dir + name;
}

} // namespace asspipe
