#pragma once
//
// style_parser.hpp — Independent ASS style/event parser.
//
// parse_ass() is the single entry point used both by the pipeline and,
// indirectly (via resolve_style()), by the layout engine, so style
// resolution logic is never duplicated.
//
#include <string>
#include <vector>

#include "ass_model.hpp"

namespace asspipe {

// Parse complete ASS text (UTF-8). `source_name` is used only in
// diagnostics. Never throws on malformed input; all problems are recorded
// in AssDocument::diagnostics. Enforces MAX_INPUT_BYTES / MAX_EVENTS.
AssDocument parse_ass(const std::string& text,
                      const std::string& source_name = "<memory>");

// Build a concrete ResolvedStyle for `style` by walking its ancestor chain
// (nearest first) and taking the first explicitly set value per field,
// then applying ASS defaults. Requires style.valid == true.
ResolvedStyle resolve_style(const AssDocument& doc, const Style& style);

// Split an ASS line into comma-separated fields. Empty fields are kept as
// empty strings; `max_fields` keeps any remaining commas inside the last
// field (used for Dialogue Text).
std::vector<std::string> split_fields(const std::string& line,
                                      std::size_t max_fields);

// Parse H:MM:SS.cc (hours may have more than one digit). Returns false on
// any malformed component; out is untouched on failure.
bool parse_timestamp(const std::string& token, long& out_centiseconds);

// Trim ASCII whitespace in place and return the same reference.
std::string& trim(std::string& s);
std::string trim_copy(std::string s);

} // namespace asspipe
