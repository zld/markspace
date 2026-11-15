#include "nal_scanner.hpp"

#include <cstring>

namespace h264 {

namespace {

// Find the start code at or after `from`; return position of first 0x00.
bool nextStartCode(const std::uint8_t* d, std::size_t n, std::size_t from,
                   std::size_t& pos, std::size_t& codeLen) {
    for (std::size_t i = from; i + 2 < n; ++i) {
        if (d[i] == 0 && d[i + 1] == 0 && d[i + 2] == 1) {
            pos = i;
            codeLen = 3;
            if (i >= 1 && d[i - 1] == 0) { pos = i - 1; codeLen = 4; }
            return true;
        }
    }
    return false;
}

ScanResult scanAnnexB(const std::uint8_t* d, std::size_t n, bool tolerant) {
    ScanResult r;
    r.format = StreamFormat::AnnexB;
    std::size_t pos = 0, codeLen = 0;
    if (!nextStartCode(d, n, 0, pos, codeLen)) {
        r.errors.push_back({0, "no Annex B start code found"});
        return r;
    }
    if (pos > 0)
        r.errors.push_back({0, "garbage bytes before first start code"});

    while (true) {
        std::size_t payload = pos + codeLen;
        std::size_t next = n, nextLen = 0;
        nextStartCode(d, n, payload, next, nextLen);
        // Trim trailing zero bytes before the next start code.
        std::size_t end = next;
        while (end > payload && d[end - 1] == 0) --end;
        if (end <= payload) {
            r.errors.push_back({pos, "empty NAL unit"});
        } else {
            NalUnit u;
            u.offset = payload;
            u.startCodeLen = codeLen;
            u.size = end - payload;
            u.header = d[payload];
            u.nalRefIdc = (u.header >> 5) & 0x3;
            u.nalType = u.header & 0x1F;
            if (u.size > MAX_NAL_SIZE)
                r.errors.push_back({pos, "NAL unit exceeds 64 MiB limit"});
            else if (u.nalType < 1 || u.nalType > 12) {
                ScanError e{pos, "unknown NAL unit type " +
                                 std::to_string(u.nalType)};
                if (tolerant) { r.errors.push_back(e); r.nals.push_back(u); }
                else { r.errors.push_back(e); return r; }
            }
            else
                r.nals.push_back(u);
        }
        if (next >= n) break;
        pos = next;
        codeLen = nextLen;
    }
    return r;
}

bool readU32(const std::uint8_t* d, std::size_t n, std::size_t at,
             std::uint32_t& out) {
    if (at + 4 > n) return false;
    out = (static_cast<std::uint32_t>(d[at]) << 24) |
          (static_cast<std::uint32_t>(d[at + 1]) << 16) |
          (static_cast<std::uint32_t>(d[at + 2]) << 8) |
          static_cast<std::uint32_t>(d[at + 3]);
    return true;
}

ScanResult scanAvcc(const std::uint8_t* d, std::size_t n, bool tolerant) {
    ScanResult r;
    r.format = StreamFormat::LengthPrefixed;
    std::size_t cursor = 0;
    while (cursor < n) {
        std::uint32_t len = 0;
        if (!readU32(d, n, cursor, len)) {
            r.errors.push_back({cursor, "truncated 4-byte length prefix"});
            break;
        }
        std::size_t payload = cursor + 4;
        if (len == 0) {
            r.errors.push_back({cursor, "zero-length NAL unit"});
            break;
        }
        if (static_cast<std::uint64_t>(payload) + len > n) {
            r.errors.push_back({cursor,
                "NAL length " + std::to_string(len) +
                " exceeds remaining bytes " + std::to_string(n - payload)});
            break;
        }
        NalUnit u;
        u.offset = payload;
        u.startCodeLen = 0;
        u.size = len;
        u.header = d[payload];
        u.nalRefIdc = (u.header >> 5) & 0x3;
        u.nalType = u.header & 0x1F;
        if (u.size > MAX_NAL_SIZE) {
            r.errors.push_back({cursor, "NAL unit exceeds 64 MiB limit"});
            if (!tolerant) break;
        } else if (u.nalType < 1 || u.nalType > 12) {
            r.errors.push_back({cursor, "unknown NAL unit type " +
                                        std::to_string(u.nalType)});
            if (!tolerant) break;
            r.nals.push_back(u);
        } else {
            r.nals.push_back(u);
        }
        cursor = payload + len;
    }
    return r;
}

StreamFormat detectFormat(const std::uint8_t* d, std::size_t n) {
    if (n >= 3 && d[0] == 0 && d[1] == 0 && d[2] == 1)
        return StreamFormat::AnnexB;
    if (n >= 4 && d[0] == 0 && d[1] == 0 && d[2] == 0 && d[3] == 1)
        return StreamFormat::AnnexB;
    // Length-prefixed probe: first u32 must describe a plausible NAL whose
    // header type is known and whose end lines up with the buffer (one unit)
    // or with another valid length prefix.
    if (n >= 5) {
        std::uint32_t len = (static_cast<std::uint32_t>(d[0]) << 24) |
                            (static_cast<std::uint32_t>(d[1]) << 16) |
                            (static_cast<std::uint32_t>(d[2]) << 8) |
                            d[3];
        if (len >= 1 && len <= MAX_NAL_SIZE &&
            static_cast<std::uint64_t>(len) + 4 <= n) {
            std::uint8_t hdr = d[4];
            std::uint8_t t = hdr & 0x1F;
            bool knownType = t >= 1 && t <= 12;
            bool exactFit = static_cast<std::uint64_t>(len) + 4 == n;
            bool chained = false;
            std::size_t at = 4u + len;
            if (!exactFit && at + 4 <= n) {
                std::uint32_t len2 = 0;
                if (readU32(d, n, at, len2) &&
                    len2 >= 1 &&
                    static_cast<std::uint64_t>(at) + 4 + len2 <= n) {
                    std::uint8_t t2 = d[at + 4] & 0x1F;
                    chained = t2 >= 1 && t2 <= 12;
                }
            }
            if (knownType && (exactFit || chained))
                return StreamFormat::LengthPrefixed;
        }
    }
    return StreamFormat::Unknown;
}

} // namespace

ScanResult scanNals(const std::uint8_t* data, std::size_t size,
                    const std::string& formatHint, bool tolerant) {
    ScanResult r;
    if (size == 0) {
        r.errors.push_back({0, "empty input"});
        return r;
    }
    if (size > MAX_FILE_SIZE) {
        r.errors.push_back({0, "file exceeds 512 MiB limit"});
        return r;
    }
    StreamFormat detected = detectFormat(data, size);
    if (formatHint == "annexb") {
        if (detected == StreamFormat::LengthPrefixed) {
            r.errors.push_back({0,
                "format hint annexb conflicts with length-prefixed data"});
            return r;
        }
        return scanAnnexB(data, size, tolerant);
    }
    if (formatHint == "avcc") {
        if (detected == StreamFormat::AnnexB) {
            r.errors.push_back({0,
                "format hint avcc conflicts with Annex B data"});
            return r;
        }
        return scanAvcc(data, size, tolerant);
    }
    // auto
    if (detected == StreamFormat::Unknown) {
        r.errors.push_back({0,
            "cannot determine stream format (neither start codes nor "
            "consistent 4-byte length prefixes)"});
        return r;
    }
    return detected == StreamFormat::AnnexB
               ? scanAnnexB(data, size, tolerant)
               : scanAvcc(data, size, tolerant);
}

const char* formatName(StreamFormat f) {
    switch (f) {
        case StreamFormat::AnnexB: return "annexb";
        case StreamFormat::LengthPrefixed: return "avcc";
        default: return "unknown";
    }
}

} // namespace h264
