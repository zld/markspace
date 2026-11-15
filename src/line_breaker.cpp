// Cluster segmentation and DP line breaking. See header for the contract.

#include "subtitle/line_breaker.hpp"

#include <limits>

#include "subtitle/utf8.hpp"

namespace subtitle {

std::vector<Cluster> segment_clusters(const std::string& text) {
  std::vector<Cluster> out;
  for (const auto& d : utf8::decode(text)) {
    const std::uint32_t cp = d.cp;
    if (!out.empty() && utf8::is_cluster_extender(cp) &&
        out.back().kind != Cluster::Newline) {
      out.back().byte_len += d.len;  // extend previous cluster
      continue;
    }
    Cluster c;
    c.byte_offset = d.offset;
    c.byte_len = d.len;
    c.first_cp = cp;
    if (cp == '\n') {
      c.kind = Cluster::Newline;
    } else if (cp == ' ' || cp == '\t') {
      c.kind = Cluster::Space;
    } else if (utf8::is_no_line_end(cp)) {
      c.kind = Cluster::PunctOpen;
    } else if (utf8::is_no_line_start(cp)) {
      c.kind = Cluster::PunctClose;
    } else if (utf8::is_cjk(cp)) {
      c.kind = Cluster::Cjk;
    } else {
      c.kind = Cluster::Word;
    }
    out.push_back(c);
  }
  return out;
}

namespace {

// May a line break happen between clusters i and i+1?
bool break_allowed(const std::vector<Cluster>& cs, std::size_t i) {
  const Cluster& a = cs[i];
  const Cluster& b = cs[i + 1];
  if (a.kind == Cluster::Space) return true;
  if (b.kind == Cluster::Space) return true;
  if (a.kind == Cluster::Word && b.kind == Cluster::Word) return false;
  if (a.kind == Cluster::PunctOpen) return false;
  if (b.kind == Cluster::PunctClose) return false;
  return true;
}

}  // namespace

BreakResult break_lines(const std::vector<Cluster>& clusters,
                        const WidthFn& width, Fixed max_width) {
  BreakResult result;
  const std::size_t n = clusters.size();

  // Paragraph boundaries at explicit newlines.
  std::vector<std::pair<std::size_t, std::size_t>> paragraphs;
  std::size_t p0 = 0;
  for (std::size_t i = 0; i <= n; ++i) {
    if (i == n || clusters[i].kind == Cluster::Newline) {
      paragraphs.emplace_back(p0, i);
      p0 = i + 1;
    }
  }

  // Cost model: fitting lines pay slack^2 (raggedness balance); an
  // over-wide line pays more than any fitting line (max_width^2) plus
  // excess^2, so breaking still happens when nothing fits and unbreakable
  // words simply form their own over-wide line.
  const double mw = static_cast<double>(max_width);
  const double kOverflow = mw * mw + 1.0;
  for (const auto& par : paragraphs) {
    const std::size_t lo = par.first, hi = par.second;
    if (lo == hi) {  // empty paragraph: blank line
      result.lines.emplace_back(lo, lo);
      result.fits.push_back(true);
      continue;
    }
    const std::size_t m = hi - lo;
    std::vector<double> dp(m + 1, std::numeric_limits<double>::infinity());
    std::vector<std::size_t> parent(m + 1, 0);
    dp[0] = 0.0;
    for (std::size_t j = 1; j <= m; ++j) {
      for (std::size_t i = 0; i < j; ++i) {
        if (dp[i] == std::numeric_limits<double>::infinity()) continue;
        if (j < m && !break_allowed(clusters, lo + j - 1)) continue;
        // Trim spaces at both ends of the candidate line.
        std::size_t a = lo + i, b = lo + j;
        while (a < b && clusters[a].kind == Cluster::Space) ++a;
        while (b > a && clusters[b - 1].kind == Cluster::Space) --b;
        if (a == b) continue;
        const Fixed w = width(a, b);
        double cost;
        if (w <= max_width) {
          const double slack = mw - static_cast<double>(w);
          cost = slack * slack;
        } else {
          const double excess = static_cast<double>(w) - mw;
          cost = kOverflow + excess * excess;
        }
        if (dp[i] + cost < dp[j]) {
          dp[j] = dp[i] + cost;
          parent[j] = i;
        }
      }
    }
    // Reconstruct.
    std::vector<std::pair<std::size_t, std::size_t>> rev;
    for (std::size_t j = m; j > 0;) {
      const std::size_t i = parent[j];
      std::size_t a = lo + i, b = lo + j;
      while (a < b && clusters[a].kind == Cluster::Space) ++a;
      while (b > a && clusters[b - 1].kind == Cluster::Space) --b;
      rev.emplace_back(a, b);
      j = i;
    }
    for (auto it = rev.rbegin(); it != rev.rend(); ++it) {
      result.lines.push_back(*it);
      result.fits.push_back(width(it->first, it->second) <= max_width);
    }
  }
  return result;
}

}  // namespace subtitle
