#include "line_breaker.hpp"

#include <algorithm>

namespace sm {

namespace {

bool isCjk(char32_t cp) {
    return (cp >= 0x2E80 && cp <= 0x9FFF) ||
           (cp >= 0xAC00 && cp <= 0xD7AF) ||
           (cp >= 0xF900 && cp <= 0xFAFF) ||
           (cp >= 0xFF00 && cp <= 0xFFEF) ||
           (cp >= 0x1F200 && cp <= 0x1F2FF) ||
           (cp >= 0x20000 && cp <= 0x2FFFF);
}

// Closing punctuation, full stops and small kana: may not start a line.
bool isNoLineStart(char32_t cp) {
    if (cp == ',' || cp == '.' || cp == '!' || cp == '?' || cp == ';' ||
        cp == ':' || cp == ')' || cp == ']' || cp == '}' ||
        cp == 0x2019 || cp == 0x201D || cp == 0x2026)
        return true;
    if (cp >= 0x3001 && cp <= 0x3003) return true;       // 、。〃
    if (cp >= 0x3009 && cp <= 0x300F) return true;       // 》」』
    if (cp == 0x3011) return true;                        // 】
    if (cp == 0xFF01 || cp == 0xFF1F || cp == 0xFF0C ||  // ！？，
        cp == 0xFF0E || cp == 0xFF1A || cp == 0xFF1B)    // ．：；
        return true;
    if (cp == 0xFF09 || cp == 0xFF3D || cp == 0xFF5D)    // )]} fullwidth
        return true;
    if (cp >= 0x3041 && cp <= 0x3042) return true;       // small kana ぁあ
    if (cp == 0x3044 || cp == 0x3046 || cp == 0x3048 ||
        cp == 0x304A || (cp >= 0x3095 && cp <= 0x3096) ||
        (cp >= 0x30A1 && cp <= 0x30A2) || cp == 0x30A4 ||
        cp == 0x30A6 || cp == 0x30A8 || cp == 0x30AA ||
        (cp >= 0x30FC && cp <= 0x30FD))
        return true;
    return false;
}

// Opening punctuation: may not end a line.
bool isNoLineEnd(char32_t cp) {
    if (cp == '(' || cp == '[' || cp == '{' || cp == 0x2018 ||
        cp == 0x201C)
        return true;
    if (cp == 0x3008 || cp == 0x300A || cp == 0x300B ||  // 《〈〉
        cp == 0x300D || cp == 0x3010)                     // 」【
        return true;
    if (cp == 0xFF08 || cp == 0xFF3B || cp == 0xFF5B)    // ([{ fullwidth
        return true;
    return false;
}

}  // namespace

LineBreaker::LineBreaker(const BreakInput& input) : in_(input) {}

bool LineBreaker::isHardNewline(std::size_t i) const {
    return in_.clusters[i].is_newline;
}

bool LineBreaker::isSpaceCluster(std::size_t i) const {
    return in_.clusters[i].is_space;
}

char32_t LineBreaker::firstInkCodepoint(std::size_t i) const {
    for (const auto& c : in_.clusters[i].cps) {
        if (c.value != 0x200B && c.value != 0x200C &&
            c.value != 0x200D)
            return c.value;
    }
    return in_.clusters[i].cps.front().value;
}

char32_t LineBreaker::lastInkCodepoint(std::size_t i) const {
    for (auto it = in_.clusters[i].cps.rbegin();
         it != in_.clusters[i].cps.rend(); ++it) {
        if (it->value != 0x200B && it->value != 0x200C &&
            it->value != 0x200D)
            return it->value;
    }
    return in_.clusters[i].cps.back().value;
}

bool LineBreaker::allowedAtLineStart(std::size_t i) const {
    return !isNoLineStart(firstInkCodepoint(i));
}

bool LineBreaker::allowedAtLineEnd(std::size_t i) const {
    return !isNoLineEnd(lastInkCodepoint(i));
}

bool LineBreaker::isCjkOpportunity(std::size_t boundary) const {
    const char32_t prev = lastInkCodepoint(boundary - 1);
    const char32_t next = firstInkCodepoint(boundary);
    return isCjk(prev) || isCjk(next);
}

bool LineBreaker::legalBreak(std::size_t boundary) const {
    // A break sits between boundary-1 and boundary.
    if (boundary == 0) return false;
    if (isHardNewline(boundary - 1)) return false;
    if (isSpaceCluster(boundary - 1) || isSpaceCluster(boundary))
        return true;
    if (in_.policy == WrapPolicy::Hard) return true;
    if (in_.policy == WrapPolicy::Space) return false;
    // Punctuation policy: kinsoku rules on top of CJK boundaries.
    if (!isCjkOpportunity(boundary)) return false;
    return allowedAtLineEnd(boundary - 1) &&
           allowedAtLineStart(boundary);
}

std::vector<LaidLine> LineBreaker::run() {
    std::vector<LaidLine> lines;
    const std::size_t n = in_.clusters.size();
    std::size_t pos = 0;

    while (pos < n) {
        LaidLine line;
        line.first_cluster = pos;

        // A cluster wider than the whole frame occupies its own line; no
        // rewind can make it fit. This guarantees progress (one cluster per
        // iteration) and rules out any infinite loop on pathological input.
        if (!isHardNewline(pos) &&
            in_.measurements[pos].advance > in_.content_width) {
            line.cluster_count = 1;
            line.width = in_.measurements[pos].advance;
            line.overflow = true;
            line.overflow_reason = "cluster_too_wide";
            lines.push_back(line);
            ++pos;
            continue;
        }

        double width = 0.0;
        std::size_t count = 0;
        std::size_t last_break = 0;  // cluster count at last usable cut
        bool overflowed = false;

        // Greedy scan. A boundary is a usable cut only when it is both a
        // policy-level break opportunity and kinsoku-legal. If the scan
        // reaches the paragraph end without any cut, the whole accumulated
        // run stays on one (overflowing) line: clusters are never split and
        // every iteration still consumes at least one cluster.
        while (pos < n && !isHardNewline(pos)) {
            const double cw = in_.measurements[pos].advance;
            if (cw > in_.content_width) break;  // handled by outer loop
            if (count > 0 && width + cw > in_.content_width) {
                overflowed = true;
                break;
            }
            width += cw;
            ++count;
            ++pos;
            if (pos < n && !isHardNewline(pos) && legalBreak(pos) &&
                allowedAtLineStart(pos) && allowedAtLineEnd(pos - 1))
                last_break = count;
        }

        if (overflowed) {
            if (last_break > 0) {
                // Rewind to the last usable cut (for hard policy every
                // boundary qualifies); remaining clusters are reconsidered
                // by the next outer iteration.
                pos = line.first_cluster + last_break;
                count = last_break;
            } else {
                // No legal cut exists before the overflowing cluster. Push
                // the remaining paragraph clusters onto this same line so
                // nothing is orphaned, and report the overflow.
                line.overflow_reason =
                    in_.policy == WrapPolicy::Hard
                        ? "cluster_too_wide"
                        : "no_break_opportunity";
                while (pos < n && !isHardNewline(pos)) {
                    width += in_.measurements[pos].advance;
                    ++count;
                    ++pos;
                }
                line.overflow = true;
            }
        }

        line.cluster_count = count;
        // Trim trailing spaces from the reported width.
        std::size_t end = line.first_cluster + count;
        std::size_t ink_end = end;
        while (ink_end > line.first_cluster &&
               isSpaceCluster(ink_end - 1))
            --ink_end;
        line.width = 0.0;
        for (std::size_t k = line.first_cluster; k < ink_end; ++k)
            line.width += in_.measurements[k].advance;

        // A cluster wider than the whole safe area is always an overflow.
        for (std::size_t k = line.first_cluster; k < end; ++k) {
            if (in_.measurements[k].advance > in_.content_width) {
                line.overflow = true;
                line.overflow_reason = "cluster_too_wide";
            }
        }
        lines.push_back(line);

        if (pos < n && isHardNewline(pos)) ++pos;  // consume paragraph break
    }

    if (lines.empty()) lines.push_back(LaidLine{});  // empty text -> zero box
    return lines;
}

}  // namespace sm
