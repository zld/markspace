#pragma once
//
// ass_model.hpp — Core data model for the ASS subtitle pipeline.
//
// All structures are plain value types produced by the parser/tag resolver
// and consumed by the layout engine, renderer and CLI entry point.
//
#include <cstdint>
#include <map>
#include <string>
#include <vector>

namespace asspipe {

// Hard limits required by the specification.
constexpr std::size_t MAX_INPUT_BYTES = 32u * 1024u * 1024u; // 32 MiB
constexpr std::size_t MAX_EVENTS = 10'000u;

// Diagnostic categories. Error-level diagnostics make the input invalid
// (exit code 2); warnings are reported but do not block rendering.
enum class DiagLevel { Warning, Error };

enum class DiagKind {
    MissingParent,     // inherited parent style does not exist
    SelfInheritance,   // style declares itself as parent
    InheritanceCycle,  // two or more styles form a cycle
    UnclosedBrace,     // override block '{' has no matching '}'
    BadTimestamp,      // malformed H:MM:SS.cc timestamp
    BadFieldCount,     // too few comma-separated fields
    TooManyEvents,     // event count exceeds MAX_EVENTS
    InputTooLarge,     // file exceeds MAX_INPUT_BYTES
    InvalidSection,    // unknown/unsupported section header
    FontMissing,       // requested font configuration unavailable
    PathRejected,      // output path collides with an input path
    RenderFailure      // libass rendering failure
};

struct Diagnostic {
    DiagLevel level = DiagLevel::Error;
    DiagKind kind = DiagKind::BadTimestamp;
    int line = 0;        // 1-based ASS file line, 0 when not file-bound
    int event_index = 0; // 1-based event index, 0 when not event-bound
    std::string message;

    std::string category() const; // stable machine-readable name
};

// One parsed [V4+ Styles] row. Empty string fields mean "not specified",
// so the resolver can walk the ancestor chain for a value.
struct Style {
    std::string name;
    int line = 0;

    std::string fontname;          // empty -> inherit/default
    int fontsize = 0;              // 0 -> inherit/default
    std::string primary_colour;    // &HAABBGGRR, empty -> default
    std::string secondary_colour;
    std::string outline_colour;
    std::string back_colour;
    int bold = -1;                 // -1 unspecified, 0 off, 1 on
    int italic = -1;
    int border_style = 1;
    double outline = 0.0;
    double shadow = 0.0;
    int alignment = 2;             // ASS numpad alignment, default 2
    int margin_l = -1;
    int margin_r = -1;
    int margin_v = -1;
    int encoding = 1;

    std::string parent;            // raw inheritance target, "" if none
    std::vector<std::string> ancestors; // resolved chain, nearest first
    bool valid = true;             // false when missing/cyclic
};

// Counts per event for the inline-override tag statistics.
struct TagCounts {
    int r = 0;     // \r reset
    int b = 0;     // \b bold
    int i = 0;     // \i italic
    int fs = 0;    // \fs font size
    int fn = 0;    // \fn font name
    int c = 0;     // \c / \1c..\4c colour
    int alpha = 0; // \alpha / \1a..\4a
    int pos = 0;   // \pos
    int an = 0;    // \an
    int unknown = 0;
    int total() const {
        return r + b + i + fs + fn + c + alpha + pos + an + unknown;
    }
};

// One parsed [Events] Dialogue row.
struct Event {
    int index = 0;                 // 1-based dialogue index
    int line = 0;                  // ASS source line
    long start_cs = 0;             // start time, centiseconds
    long end_cs = 0;
    std::string style_name;
    std::string text;              // raw ASS text incl. override blocks
    TagCounts tags;
    bool unclosed_block = false;
};

// Fully resolved style after ancestor-chain resolution: every field has a
// concrete value, ready for libass and for the layout engine.
struct ResolvedStyle {
    std::string name;
    std::string fontname;
    int fontsize = 0;
    int bold = 0;
    int italic = 0;
    std::string primary_colour;
    std::string outline_colour;
    int alignment = 2;
    int margin_l = 0;
    int margin_r = 0;
    int margin_v = 0;
    double outline = 0.0;
    double shadow = 0.0;
};

struct AssDocument {
    std::map<std::string, Style> styles;      // keyed by lower-case name
    std::vector<Event> events;
    std::vector<Diagnostic> diagnostics;
    int inline_tags = 0;
    int unknown_tags = 0;
    int inheritance_errors = 0;
};

} // namespace asspipe
