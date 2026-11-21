#include "line_breaker.h"

#include <algorithm>
#include <cstdio>
#include <limits>

namespace {

bool is_hard_break(const Cluster& cluster) {
    return cluster.first == '\n' || cluster.first == '\r' ||
           cluster.first == 0x2028 || cluster.first == 0x2029 ||
           cluster.first == 0x85;
}

std::string code_point_label(char32_t cp) {
    char buffer[16];
    std::snprintf(buffer, sizeof(buffer), "U+%04X", static_cast<unsigned>(cp));
    return buffer;
}

}  // namespace

bool is_preferred_break(const Cluster& previous, const Cluster& next) {
    if (is_extending_cluster(next) || is_hard_break(next)) {
        return false;
    }
    return previous.kind == ClusterKind::Whitespace ||
           previous.kind == ClusterKind::Punctuation;
}

ShapedText shape_clusters(const std::vector<Cluster>& clusters,
                          const std::vector<char32_t>& codepoints,
                          const FontMetrics& metrics) {
    ShapedText shaped;
    shaped.advances.assign(clusters.size(), 0);
    std::vector<std::string> seen;
    const int spacing = metrics.letter_spacing();
    for (std::size_t i = 0; i < clusters.size(); ++i) {
        long long total = 0;
        bool drew_glyph = false;
        for (std::size_t k = 0; k < clusters[i].codepoint_count; ++k) {
            const std::size_t at = clusters[i].codepoint_begin + k;
            if (at >= codepoints.size()) {
                break;
            }
            const char32_t cp = codepoints[at];
            if (cp == '\n' || cp == '\r' || cp == 0x2028 || cp == 0x2029 ||
                cp == 0x85) {
                continue;  // line breaks have no drawn glyph
            }
            GlyphInfo glyph;
            if (!metrics.glyph(cp, glyph)) {
                shaped.errors.push_back("font metric lookup failed for cluster " +
                                        std::to_string(i));
                continue;
            }
            if (glyph.missing && is_default_ignorable(cp)) {
                // Variation selectors and joiners never draw; a face that
                // lacks them is not a substitution the reviewer must fix.
                continue;
            }
            drew_glyph = true;
            if (glyph.missing) {
                // The substitute .notdef box is what a renderer would draw, so
                // its advance belongs in the safe-area measurement. The
                // substitution is also counted once per distinct code point.
                const std::string label = code_point_label(cp);
                if (std::find(seen.begin(), seen.end(), label) == seen.end()) {
                    seen.push_back(label);
                    shaped.missing_codepoints.push_back(label);
                    ++shaped.missing_glyphs;
                }
                total += glyph.advance;
            } else {
                total += glyph.advance;
            }
        }
        // Letter spacing sits between clusters, not between code points, so
        // a combining sequence is one stepped unit.
        if (drew_glyph) {
            total += spacing;
        }
        shaped.advances[i] = total;
    }
    return shaped;
}

TextLayout layout_clusters(const std::vector<Cluster>& clusters,
                           const ShapedText& shaped, const FontMetrics& metrics,
                           int max_width, int max_lines) {
    TextLayout result;
    const std::size_t n = clusters.size();
    const long long spacing = metrics.letter_spacing();
    const std::vector<long long>& advance = shaped.advances;

    auto width_of = [&](std::size_t begin, std::size_t end) {
        long long total = 0;
        for (std::size_t i = begin; i < end && i < advance.size(); ++i) {
            total += advance[i];
        }
        if (end > begin) {
            total -= spacing;
        }
        return static_cast<int>(std::max<long long>(0, total));
    };
    auto trim_end = [&](std::size_t begin, std::size_t end) {
        while (end > begin && clusters[end - 1].kind == ClusterKind::Whitespace) {
            --end;
        }
        return end;
    };

    std::vector<LineLayout> lines;
    auto add_line = [&](std::size_t begin, std::size_t end,
                        const std::string& reason, const std::string& overflow) {
        const std::size_t visible_end = trim_end(begin, end);
        LineLayout line;
        line.begin_cluster = begin;
        line.end_cluster = visible_end;
        line.width = width_of(begin, visible_end);
        line.break_reason = reason;
        line.overflow_reason = overflow;
        if (!overflow.empty()) {
            ++result.overflow_count;
        }
        result.max_line_width = std::max(result.max_line_width, line.width);
        lines.push_back(line);
    };

    std::size_t start = 0;
    while (start < n) {
        // A wrapped line never starts with the whitespace it broke on.
        while (start < n && start > 0 &&
               clusters[start].kind == ClusterKind::Whitespace &&
               !is_hard_break(clusters[start])) {
            ++start;
        }
        if (start >= n) {
            break;
        }
        if (is_hard_break(clusters[start])) {
            add_line(start, start, "explicit", "");
            ++start;
            continue;
        }
        std::size_t fitting_end = start;
        std::size_t preferred_end = std::numeric_limits<std::size_t>::max();
        std::string preferred_reason;
        long long width = -spacing;
        std::size_t i = start;
        bool first_cluster_overflows = false;
        for (; i < n; ++i) {
            if (is_hard_break(clusters[i])) {
                break;
            }
            const long long candidate = width + advance[i];
            if (candidate > max_width) {
                first_cluster_overflows = i == start;
                break;
            }
            width = candidate;
            fitting_end = i + 1;
            if (i + 1 < n && is_preferred_break(clusters[i], clusters[i + 1])) {
                preferred_end = i + 1;
                preferred_reason =
                    clusters[i].kind == ClusterKind::Whitespace ? "space"
                                                                : "punctuation";
            }
        }
        if (first_cluster_overflows) {
            add_line(start, start + 1, "fallback", "cluster_too_wide");
            result.errors.push_back(
                "cluster " + std::to_string(start) + " is " +
                std::to_string(advance[start]) + " px wide, over the " +
                std::to_string(max_width) + " px limit");
            ++start;
            continue;
        }
        if (i < n && is_hard_break(clusters[i])) {
            add_line(start, i, "explicit", "");
            start = i + 1;
            continue;
        }
        if (i >= n) {
            add_line(start, n, "end", "");
            break;
        }
        if (preferred_end != std::numeric_limits<std::size_t>::max() &&
            preferred_end > start) {
            add_line(start, preferred_end, preferred_reason, "");
            start = preferred_end;
        } else {
            add_line(start, fitting_end, "fallback", "");
            start = fitting_end;
        }
    }
    if (max_lines > 0 && static_cast<int>(lines.size()) > max_lines) {
        ++result.overflow_count;
        result.errors.push_back(
            "content needs " + std::to_string(lines.size()) +
            " lines but max_lines is " + std::to_string(max_lines));
        // Tag the overflow on the first line past the limit so the summary
        // can point at it.
        lines[static_cast<std::size_t>(max_lines)].overflow_reason = "max_lines";
    }
    result.lines = std::move(lines);
    return result;
}
