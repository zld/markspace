#pragma once

// Serializes LayoutEngine results. The exporter never recomputes widths
// or line breaks; it only formats what the engine produced.

#include <string>
#include <vector>

#include "subtitle/model.hpp"

namespace subtitle {

// One JSON object per cue (no trailing newline).
std::string cue_to_jsonl(const Cue& cue, const CueLayout& layout);

std::string summary_to_json(const Summary& summary);

// Writes one JSONL record per cue, in input order. "-" means stdout.
// Returns false on any I/O failure.
bool write_jsonl(const std::string& path, const std::vector<Cue>& cues,
                 const std::vector<CueLayout>& layouts);

// Writes the summary JSON via a same-directory temp file + rename, so a
// failed run never clobbers a previous good summary.
bool write_summary_atomic(const std::string& path, const Summary& summary);

}  // namespace subtitle
