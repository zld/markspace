#include "analyzer.hpp"

#include <algorithm>
#include <array>
#include <cstdint>

namespace sm {

namespace {

// --- SHA-256 (FIPS 180-4), implemented locally to avoid extra deps -------
constexpr std::array<uint32_t, 64> kSha256K = {
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1,
    0x923f82a4, 0xab1c5ed5, 0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
    0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174, 0xe49b69c1, 0xefbe4786,
    0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147,
    0x06ca6351, 0x14292967, 0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
    0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85, 0xa2bfe8a1, 0xa81a664b,
    0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a,
    0x5b9cca4f, 0x682e6ff3, 0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
    0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2};

uint32_t rotr(uint32_t x, uint32_t n) { return (x >> n) | (x << (32 - n)); }

}  // namespace

std::string sha256Hex(const std::string& data) {
    std::array<uint32_t, 8> h = {0x6a09e667, 0xbb67ae85, 0x3c6ef372,
                                 0xa54ff53a, 0x510e527f, 0x9b05688c,
                                 0x1f83d9ab, 0x5be0cd19};
    std::string msg = data;
    const uint64_t bit_len = static_cast<uint64_t>(data.size()) * 8;
    msg.push_back(static_cast<char>(0x80));
    while (msg.size() % 64 != 56) msg.push_back('\0');
    for (int i = 7; i >= 0; --i)
        msg.push_back(static_cast<char>((bit_len >> (8 * i)) & 0xFF));

    for (std::size_t chunk = 0; chunk < msg.size(); chunk += 64) {
        std::array<uint32_t, 64> w{};
        for (int i = 0; i < 16; ++i) {
            const auto* p =
                reinterpret_cast<const unsigned char*>(&msg[chunk + i * 4]);
            w[i] = (uint32_t(p[0]) << 24) | (uint32_t(p[1]) << 16) |
                   (uint32_t(p[2]) << 8) | uint32_t(p[3]);
        }
        for (int i = 16; i < 64; ++i) {
            const uint32_t s0 = rotr(w[i - 15], 7) ^ rotr(w[i - 15], 18) ^
                                (w[i - 15] >> 3);
            const uint32_t s1 = rotr(w[i - 2], 17) ^ rotr(w[i - 2], 19) ^
                                (w[i - 2] >> 10);
            w[i] = w[i - 16] + s0 + w[i - 7] + s1;
        }
        uint32_t a = h[0], b = h[1], c = h[2], d = h[3];
        uint32_t e = h[4], f = h[5], g = h[6], hh = h[7];
        for (int i = 0; i < 64; ++i) {
            const uint32_t S1 = rotr(e, 6) ^ rotr(e, 11) ^ rotr(e, 25);
            const uint32_t ch = (e & f) ^ (~e & g);
            const uint32_t t1 = hh + S1 + ch + kSha256K[i] + w[i];
            const uint32_t S0 = rotr(a, 2) ^ rotr(a, 13) ^ rotr(a, 22);
            const uint32_t maj = (a & b) ^ (a & c) ^ (b & c);
            const uint32_t t2 = S0 + maj;
            hh = g; g = f; f = e; e = d + t1;
            d = c; c = b; b = a; a = t1 + t2;
        }
        h[0] += a; h[1] += b; h[2] += c; h[3] += d;
        h[4] += e; h[5] += f; h[6] += g; h[7] += hh;
    }
    static const char* hex = "0123456789abcdef";
    std::string out(64, '0');
    for (int i = 0; i < 8; ++i)
        for (int j = 0; j < 8; ++j)
            out[i * 8 + j] = hex[(h[i] >> (28 - 4 * j)) & 0xF];
    return out;
}

Analyzer::Analyzer(const Config& cfg, FontMetrics& font)
    : cfg_(cfg),
      font_(font),
      content_width_(static_cast<double>(
          cfg.width - cfg.safe_left - cfg.safe_right)) {}

SubtitleResult Analyzer::analyze(const std::string& id,
                                 const std::string& language,
                                 const std::string& text) const {
    SubtitleResult result;
    result.id = id;
    result.language = language;
    result.sha256_hex = sha256Hex(text);

    std::vector<Codepoint> cps;
    Utf8Error utf_err;
    if (!decodeUtf8(text, cps, utf_err)) {
        // Caller validates UTF-8 per entry first; keep the guard anyway.
        result.overflows.push_back(
            {-1, "left", "invalid_utf8:" + utf_err.message, 0.0});
        return result;
    }

    BreakInput bi;
    bi.clusters = segmentGraphemes(cps);
    bi.content_width = content_width_;
    bi.policy = cfg_.wrap;
    bi.measurements.reserve(bi.clusters.size());
    for (const auto& cluster : bi.clusters) {
        if (cluster.is_newline) {
            bi.measurements.push_back(ClusterMeasurement{});
        } else {
            bi.measurements.push_back(
                font_.measureCluster(cluster, result.missing_glyphs));
        }
    }

    std::vector<LaidLine> laid = LineBreaker(bi).run();
    const double line_height = font_.lineHeight();

    double min_x = 0.0, max_x = 0.0, min_y = 0.0, max_y = 0.0;
    bool bbox_started = false;
    for (std::size_t li = 0; li < laid.size(); ++li) {
        const LaidLine& src = laid[li];
        SubtitleLine dst;
        dst.line_index = static_cast<int>(li);
        dst.first_cluster = src.first_cluster;
        dst.cluster_count = src.cluster_count;
        dst.width = src.width;
        dst.x = 0.0;
        // Top-down coordinates: line box top at li * line_height, and the
        // baseline sits `ascender` pixels below it.
        dst.y = static_cast<double>(li) * line_height;
        const double baseline_y = dst.y + font_.ascender();

        double line_min_x = 0.0, line_max_x = src.width;
        // Ink edges in top-down coordinates relative to the subtitle box.
        double line_min_y = baseline_y + font_.descender();
        double line_max_y = baseline_y;
        for (std::size_t k = 0; k < src.cluster_count; ++k) {
            const auto& m = bi.measurements[src.first_cluster + k];
            line_min_x = std::min(line_min_x, m.left_bearing);
            line_max_x = std::max(line_max_x, m.right_edge);
            line_min_y = std::min(line_min_y, baseline_y - m.top_bearing);
            line_max_y = std::max(line_max_y,
                                  baseline_y - m.bottom_edge);
        }
        dst.text.clear();
        for (std::size_t k = 0; k < src.cluster_count; ++k)
            dst.text += clusterText(bi.clusters[src.first_cluster + k]);

        if (src.cluster_count > 0) {
            if (!bbox_started) {
                min_x = line_min_x;
                max_x = line_max_x;
                min_y = line_min_y;
                max_y = line_max_y;
                bbox_started = true;
            } else {
                min_x = std::min(min_x, line_min_x);
                max_x = std::max(max_x, line_max_x);
                min_y = std::min(min_y, line_min_y);
                max_y = std::max(max_y, line_max_y);
            }
        }

        // Horizontal safe-area classification.
        if (line_max_x > content_width_) {
            result.overflows.push_back(
                {dst.line_index, "right",
                 src.overflow ? src.overflow_reason : "exceeds_safe_width",
                 line_max_x - content_width_});
        }
        if (line_min_x < 0.0) {
            result.overflows.push_back(
                {dst.line_index, "left", "negative_bearing",
                 -line_min_x});
        }
        if (src.overflow && line_max_x <= content_width_) {
            result.overflows.push_back(
                {dst.line_index, "right", src.overflow_reason,
                 content_width_ - line_max_x});
        }
        result.lines.push_back(dst);
    }

    if (bbox_started) {
        result.bbox.x = min_x;
        result.bbox.y = min_y;
        result.bbox.width = max_x - min_x;
        result.bbox.height = max_y - min_y;
    }

    // Vertical overflow relative to the configured canvas height. The
    // subtitle box starts at safe_top; check its lowest ink edge against
    // the bottom safe boundary.
    if (cfg_.height > 0 && bbox_started) {
        const double content_bottom =
            static_cast<double>(cfg_.height - cfg_.safe_bottom);
        const double content_top = static_cast<double>(cfg_.safe_top);
        const double box_bottom = content_top + max_y;
        if (box_bottom > content_bottom) {
            result.overflows.push_back(
                {-1, "bottom", "exceeds_safe_height",
                 box_bottom - content_bottom});
        }
        if (content_top + min_y < content_top) {
            result.overflows.push_back(
                {-1, "top", "exceeds_safe_height", content_top - min_y});
        }
    }

    // Deduplicate missing codepoints while preserving first-seen order.
    std::vector<char32_t> unique_missing;
    for (char32_t cp : result.missing_glyphs) {
        if (std::find(unique_missing.begin(), unique_missing.end(), cp) ==
            unique_missing.end())
            unique_missing.push_back(cp);
    }
    result.missing_glyphs = unique_missing;
    result.missing_glyph_count =
        static_cast<int>(result.missing_glyphs.size());
    return result;
}

}  // namespace sm
