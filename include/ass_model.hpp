#pragma once
// Core data structures for the ASS subtitle audit pipeline.
#include <map>
#include <string>
#include <vector>

namespace assaudit {

struct Diagnostic {
  int line = 0;
  std::string code;
  std::string message;
};

struct AssColor {
  int a = 255, r = 255, g = 255, b = 255;
  bool defined = false;
};

// Distinguishes: never provided in the chain, explicit empty string,
// explicit value, or library default applied after the chain ran dry.
enum class FieldState { Unset, ExplicitEmpty, Explicit, Defaulted };

struct StyleField {
  std::string text;
  FieldState state = FieldState::Unset;
  std::string source;       // style name that supplied the value
  int source_line = 0;
};

struct AssStyle {
  std::string name;
  int line = 0;
  bool duplicate = false;
  std::string parent;
  int parent_line = 0;
  std::map<std::string, StyleField> own;     // fields on this record only
  // Resolved by style_resolver:
  std::vector<std::string> chain;            // child first, root last
  bool blocked = false;
  std::vector<Diagnostic> errors;
  std::map<std::string, StyleField> merged;  // chain folded, with provenance
};

struct AssEvent {
  int id = 0;                                 // 1-based, dialogue only
  int line = 0;
  bool is_comment = false;
  std::string style_name;
  std::string text;
  int layer = 0;
  long long start_ms = -1;
  long long end_ms = -1;
  std::map<std::string, std::string> fields;  // all Format fields verbatim
  bool blocked = false;
  std::vector<Diagnostic> diagnostics;
};

struct TagCommand {
  std::string name;
  std::string param;
  bool unknown = false;
  bool valid = true;
  bool in_transform = false;
};

struct TagReport {
  int total = 0;
  int valid = 0;
  int unknown = 0;
  int overrides = 0;
  int nested_groups = 0;
  int unclosed_groups = 0;
  std::vector<TagCommand> commands;
  std::vector<Diagnostic> warnings;
  // Effective geometry for out-of-resolution diagnostics.
  bool has_pos = false;
  double pos_x = 0.0, pos_y = 0.0;
  bool has_move = false;
  double move_x1 = 0, move_y1 = 0, move_x2 = 0, move_y2 = 0;
};

struct EventReport {
  int event_id = 0;
  int source_line = 0;
  std::string style;
  int tag_total = 0;
  int tag_valid = 0;
  int tag_unknown = 0;
  int tag_override = 0;
  bool rendered = false;
  bool blocked = false;
  int region_width = 0;
  int region_height = 0;
  int image_blocks = 0;
  std::string pixel_digest;
  bool font_fallback = false;
  std::vector<Diagnostic> diagnostics;
};

struct AssDocument {
  std::string source_label;
  int play_res_x = 384;
  int play_res_y = 288;
  int wrap_style = 0;
  bool scaled_border = false;
  std::vector<AssStyle> styles;
  std::vector<AssEvent> events;
  std::vector<Diagnostic> diagnostics;
};

}  // namespace assaudit
