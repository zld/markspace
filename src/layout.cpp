// LayoutEngine: ties FontChain + line breaker together, producing per-cue
// ink bounding boxes, wrap offsets and overflow flags. All metrics 26.6.

#include "subtitle/layout.hpp"

#include "subtitle/font_metrics.hpp"
#include "subtitle/line_breaker.hpp"
#include "subtitle/utf8.hpp"

namespace subtitle {

struct LayoutEngine::Impl {
  FontChain fonts;
  Impl(const std::vector<std::string>& paths, double size)
      : fonts(paths, size) {}
};

LayoutEngine::LayoutEngine(const std::vector<std::string>& font_paths,
                           double font_size_px)
    : impl_(new Impl(font_paths, font_size_px)) {}

LayoutEngine::~LayoutEngine() = default;

CueLayout LayoutEngine::layout(const Cue& cue,
                               const LayoutParams& params) const {
  CueLayout out;
  out.cue_index = cue.index;
  FontChain& fonts = impl_->fonts;

  const std::vector<Cluster> clusters = segment_clusters(cue.text);
  const std::size_t n = clusters.size();

  // Per-cluster advance (incl. intra-cluster kerning) and boundary glyphs.
  std::vector<Fixed> adv(n, 0);
  std::vector<int> first_font(n, -1), last_font(n, -1);
  std::vector<std::uint32_t> first_glyph(n, 0), last_glyph(n, 0);
  std::vector<std::vector<utf8::CodePoint>> cps_of(n);
  for (std::size_t k = 0; k < n; ++k) {
    cps_of[k] = utf8::decode(cue.text.substr(clusters[k].byte_offset,
                                             clusters[k].byte_len));
    int prev_font = -1;
    std::uint32_t prev_glyph = 0;
    for (const auto& d : cps_of[k]) {
      const FontChain::GlyphInfo& g = fonts.glyph(d.cp);
      if (g.missing) ++out.missing_glyphs;
      if (utf8::needs_shaping(d.cp)) out.requires_shaping = true;
      if (prev_font >= 0 && prev_font == g.font_index)
        adv[k] += fonts.kerning(prev_font, prev_glyph, g.glyph);
      adv[k] += g.advance;
      if (first_font[k] < 0) {
        first_font[k] = g.font_index;
        first_glyph[k] = g.glyph;
      }
      prev_font = g.font_index;
      prev_glyph = g.glyph;
    }
    last_font[k] = prev_font;
    last_glyph[k] = prev_glyph;
  }

  // Prefix sums so any cluster-range width is O(1).
  std::vector<Fixed> adv_p(n + 1, 0), kern_p(n + 1, 0);
  for (std::size_t k = 0; k < n; ++k) {
    adv_p[k + 1] = adv_p[k] + adv[k];
    Fixed bk = 0;
    if (k > 0 && last_font[k - 1] >= 0 && last_font[k - 1] == first_font[k])
      bk = fonts.kerning(last_font[k - 1], last_glyph[k], first_glyph[k]);
    kern_p[k + 1] = kern_p[k] + bk;
  }
  const WidthFn width = [&](std::size_t a, std::size_t b) -> Fixed {
    return (adv_p[b] - adv_p[a]) + (kern_p[b] - kern_p[a]);
  };

  const BreakResult br = break_lines(clusters, width, params.max_width);
  const Fixed lh = fonts.line_height();

  for (std::size_t li = 0; li < br.lines.size(); ++li) {
    const auto [b, e] = br.lines[li];
    LineLayout line;
    if (b == e) {  // blank line from consecutive explicit newlines
      line.text_offset = b < n ? clusters[b].byte_offset : cue.text.size();
    } else {
      line.text_offset = clusters[b].byte_offset;
      line.text_len =
          clusters[e - 1].byte_offset + clusters[e - 1].byte_len -
          clusters[b].byte_offset;
      line.advance_width = width(b, e);
      // Ink box: walk codepoints with a running pen.
      Fixed pen = 0;
      int prev_font = -1;
      std::uint32_t prev_glyph = 0;
      for (std::size_t k = b; k < e; ++k) {
        for (const auto& d : cps_of[k]) {
          const FontChain::GlyphInfo& g = fonts.glyph(d.cp);
          if (prev_font >= 0 && prev_font == g.font_index)
            pen += fonts.kerning(prev_font, prev_glyph, g.glyph);
          if (!g.ink.empty())
            line.ink.unite(BBox{g.ink.x_min + pen, g.ink.y_min,
                                g.ink.x_max + pen, g.ink.y_max});
          pen += g.advance;
          prev_font = g.font_index;
          prev_glyph = g.glyph;
        }
      }
    }
    // Shift line ink into cue coordinates: baseline of line li sits at
    // y = -li * line_height (first line baseline is 0).
    if (!line.ink.empty()) {
      line.ink.y_min -= li * lh;
      line.ink.y_max -= li * lh;
      out.bbox.unite(line.ink);
    }
    if (li > 0) out.wrap_offsets.push_back(line.text_offset);
    if (!br.fits[li]) out.width_overflow = true;
    out.lines.push_back(line);
  }

  out.line_overflow =
      params.max_lines > 0 &&
      static_cast<int>(out.lines.size()) > params.max_lines;
  out.overflow = out.width_overflow || out.line_overflow;
  return out;
}

}  // namespace subtitle
