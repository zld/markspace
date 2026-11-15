#pragma once

#include <cstddef>
#include <string>
#include <vector>

#include "config.hpp"
#include "font_metrics.hpp"
#include "utf8.hpp"

namespace sm {

struct LaidLine {
    std::size_t first_cluster = 0;  // index into the subtitle cluster vector
    std::size_t cluster_count = 0;  // newline clusters excluded
    double width = 0.0;             // pen width excluding trailing spaces
    bool overflow = false;
    std::string overflow_reason;    // cluster_too_wide | no_break_opportunity
};

struct BreakInput {
    std::vector<GraphemeCluster> clusters;
    std::vector<ClusterMeasurement> measurements;  // parallel to clusters
    double content_width = 0.0;                    // safe-area width in px
    WrapPolicy policy = WrapPolicy::Punctuation;
};

// Greedy line breaker. Grapheme clusters are atomic: a cluster can never
// be split, and every iteration consumes at least one cluster, so the
// loop terminates even when every cluster overflows.
class LineBreaker {
public:
    explicit LineBreaker(const BreakInput& input);

    std::vector<LaidLine> run();

private:
    bool isHardNewline(std::size_t i) const;
    bool isSpaceCluster(std::size_t i) const;
    bool isCjkOpportunity(std::size_t boundary) const;
    bool allowedAtLineStart(std::size_t cluster_index) const;
    bool allowedAtLineEnd(std::size_t cluster_index) const;
    bool legalBreak(std::size_t boundary) const;
    char32_t firstInkCodepoint(std::size_t i) const;
    char32_t lastInkCodepoint(std::size_t i) const;

    const BreakInput& in_;
};

}  // namespace sm
