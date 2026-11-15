// JSONL / summary serialization. See header for the contract.

#include "subtitle/summary_exporter.hpp"

#include <cstdio>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <unistd.h>

#include <nlohmann/json.hpp>

namespace subtitle {

using nlohmann::json;

static json bbox_json(const BBox& b) {
  return json{{"x_min", b.x_min}, {"y_min", b.y_min},
              {"x_max", b.x_max}, {"y_max", b.y_max},
              {"w", b.width()},   {"h", b.height()}};
}

std::string cue_to_jsonl(const Cue& cue, const CueLayout& layout) {
  json widths = json::array();
  for (const LineLayout& l : layout.lines) widths.push_back(l.advance_width);
  json rec;
  rec["index"] = cue.index;
  rec["start_ms"] = cue.start_ms;
  rec["end_ms"] = cue.end_ms;
  rec["line_count"] = layout.lines.size();
  rec["bbox_26_6"] = bbox_json(layout.bbox);
  rec["line_widths_26_6"] = std::move(widths);
  rec["wrap_offsets_utf8"] = layout.wrap_offsets;
  rec["missing_glyphs"] = layout.missing_glyphs;
  rec["requires_shaping"] = layout.requires_shaping;
  rec["overflow"] = layout.overflow;
  rec["overflow_width"] = layout.width_overflow;
  rec["overflow_lines"] = layout.line_overflow;
  return rec.dump();
}

std::string summary_to_json(const Summary& s) {
  json j;
  j["cue_count"] = s.cue_count;
  j["overflow_entries"] = s.overflow_entries;
  j["parse_errors"] = s.parse_errors;
  j["encoding_errors"] = s.encoding_errors;
  j["missing_glyphs"] = s.missing_glyphs;
  j["shaping_cues"] = s.shaping_cues;
  return j.dump();
}

bool write_jsonl(const std::string& path, const std::vector<Cue>& cues,
                 const std::vector<CueLayout>& layouts) {
  std::ofstream file;
  std::ostream* out = &std::cout;
  if (path != "-") {
    file.open(path, std::ios::binary | std::ios::trunc);
    if (!file) return false;
    out = &file;
  }
  for (std::size_t i = 0; i < cues.size(); ++i)
    *out << cue_to_jsonl(cues[i], layouts[i]) << '\n';
  out->flush();
  return static_cast<bool>(*out);
}

bool write_summary_atomic(const std::string& path, const Summary& summary) {
  namespace fs = std::filesystem;
  const fs::path target(path);
  const fs::path tmp =
      target.parent_path() /
      (target.filename().string() + ".tmp." + std::to_string(::getpid()));
  {
    std::ofstream f(tmp, std::ios::binary | std::ios::trunc);
    if (!f) return false;
    f << summary_to_json(summary) << '\n';
    f.flush();
    if (!f) {
      std::error_code ec;
      fs::remove(tmp, ec);
      return false;
    }
  }
  std::error_code ec;
  fs::rename(tmp, target, ec);
  if (ec) {
    std::error_code ec2;
    fs::remove(tmp, ec2);
    return false;
  }
  return true;
}

}  // namespace subtitle
