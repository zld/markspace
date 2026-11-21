#include "style_parser.hpp"

#include <algorithm>
#include <cctype>
#include <set>

namespace asspipe {

namespace {

std::string lower_of(std::string s) {
    std::transform(s.begin(), s.end(), s.begin(),
                   [](unsigned char c) { return static_cast<char>(std::tolower(c)); });
    return s;
}

void mark_invalid(AssDocument& doc, const std::string& key) {
    auto it = doc.styles.find(key);
    if (it != doc.styles.end()) it->second.valid = false;
}

void diag(AssDocument& doc, DiagKind kind, int line, std::string msg,
          std::set<int>& counted_lines, bool& cycle_counted) {
    doc.diagnostics.push_back(
        Diagnostic{DiagLevel::Error, kind, line, 0, std::move(msg)});
    // Diagnostics are emitted per affected style line (so every cycle node
    // is reported with its line number), while the headline error counter
    // counts distinct defects: one per missing/self edge and one cycle
    // regardless of how many styles the cycle contains.
    if (kind == DiagKind::InheritanceCycle) {
        if (!cycle_counted) {
            cycle_counted = true;
            ++doc.inheritance_errors;
        }
    } else if (counted_lines.insert(line).second) {
        ++doc.inheritance_errors;
    }
}

// Walk one style's parent chain. `stack` holds the keys currently being
// resolved (nearest first) so cycles and repeated ancestors are detectable.
// `counted_lines` deduplicates per-style diagnostics; `cycle_counted` makes
// the headline counter treat a whole cycle as a single defect.
bool walk(AssDocument& doc, const std::string& key,
          std::vector<std::string> stack, std::set<int>& counted_lines,
          bool& cycle_counted) {
    auto it = doc.styles.find(key);
    if (it == doc.styles.end()) return false;
    Style& st = it->second;

    if (std::find(stack.begin(), stack.end(), key) != stack.end()) {
        for (const auto& k : stack) {
            mark_invalid(doc, k);
            auto sit = doc.styles.find(k);
            int line = sit == doc.styles.end() ? 0 : sit->second.line;
            // Gate by line so a cycle reached from several roots reports
            // each participating style only once.
            if (counted_lines.insert(line).second)
                diag(doc, DiagKind::InheritanceCycle, line,
                     "style '" +
                         (sit == doc.styles.end() ? k : sit->second.name) +
                         "' participates in an inheritance cycle",
                     counted_lines, cycle_counted);
        }
        return false;
    }
    stack.push_back(key);

    if (st.parent.empty()) {
        st.ancestors.clear(); // root style: empty chain
        return true;
    }
    if (lower_of(st.parent) == key) {
        st.valid = false;
        diag(doc, DiagKind::SelfInheritance, st.line,
             "style '" + st.name + "' inherits from itself", counted_lines, cycle_counted);
        return false;
    }
    auto pit = doc.styles.find(lower_of(st.parent));
    if (pit == doc.styles.end()) {
        st.valid = false;
        diag(doc, DiagKind::MissingParent, st.line,
             "style '" + st.name + "' references missing parent '" +
                 st.parent + "'",
             counted_lines, cycle_counted);
        return false;
    }
    if (!walk(doc, pit->first, stack, counted_lines, cycle_counted)) {
        st.valid = false;
        return false;
    }
    // Parent resolved successfully: chain = parent + parent's ancestors.
    st.ancestors.clear();
    st.ancestors.push_back(pit->first);
    for (const auto& a : pit->second.ancestors) st.ancestors.push_back(a);
    return true;
}

} // namespace

std::string Diagnostic::category() const {
    switch (kind) {
    case DiagKind::MissingParent: return "missing_parent";
    case DiagKind::SelfInheritance: return "self_inheritance";
    case DiagKind::InheritanceCycle: return "inheritance_cycle";
    case DiagKind::UnclosedBrace: return "unclosed_brace";
    case DiagKind::BadTimestamp: return "bad_timestamp";
    case DiagKind::BadFieldCount: return "bad_field_count";
    case DiagKind::TooManyEvents: return "too_many_events";
    case DiagKind::InputTooLarge: return "input_too_large";
    case DiagKind::InvalidSection: return "invalid_section";
    case DiagKind::FontMissing: return "font_missing";
    case DiagKind::PathRejected: return "path_rejected";
    case DiagKind::RenderFailure: return "render_failure";
    }
    return "unknown";
}

void build_inheritance_graph(AssDocument& doc) {
    doc.inheritance_errors = 0;
    for (auto& [key, st] : doc.styles) {
        st.ancestors.clear();
        st.valid = true;
    }
    std::set<int> counted_lines;
    bool cycle_counted = false;
    for (auto& [key, st] : doc.styles) {
        if (st.ancestors.empty() && st.parent.empty()) continue; // root
        walk(doc, key, {}, counted_lines, cycle_counted);
    }
}

ResolvedStyle resolve_style(const AssDocument& doc, const Style& st) {
    ResolvedStyle out;
    out.name = st.name;

    // Search order: the style itself, then each ancestor (nearest first).
    auto chain = [&]() {
        std::vector<const Style*> v;
        v.push_back(&st);
        for (const auto& a : st.ancestors) {
            auto it = doc.styles.find(a);
            if (it != doc.styles.end()) v.push_back(&it->second);
        }
        return v;
    }();

    auto pick_str = [&](auto getter) {
        for (const Style* s : chain) {
            std::string v = getter(s);
            if (!v.empty()) return v;
        }
        return std::string{};
    };
    auto pick_int = [&](int Style::*member, int unset) {
        for (const Style* s : chain)
            if (s->*member != unset) return s->*member;
        return unset;
    };
    auto pick_real = [&](double Style::*member) {
        for (const Style* s : chain)
            if (s->*member != 0.0) return s->*member;
        return 0.0;
    };

    out.fontname = pick_str([](const Style* s) { return s->fontname; });
    if (out.fontname.empty()) out.fontname = "Arial";
    out.fontsize = pick_int(&Style::fontsize, 0);
    if (out.fontsize <= 0) out.fontsize = 48; // ASS PlayRes default size
    out.bold = pick_int(&Style::bold, -1) == 1 ? 1 : 0;
    out.italic = pick_int(&Style::italic, -1) == 1 ? 1 : 0;
    out.primary_colour =
        pick_str([](const Style* s) { return s->primary_colour; });
    if (out.primary_colour.empty()) out.primary_colour = "&H00FFFFFF";
    out.outline_colour =
        pick_str([](const Style* s) { return s->outline_colour; });
    if (out.outline_colour.empty()) out.outline_colour = "&H00000000";
    out.alignment = pick_int(&Style::alignment, 0);
    if (out.alignment < 1 || out.alignment > 9) out.alignment = 2;
    out.margin_l = pick_int(&Style::margin_l, -1);
    out.margin_r = pick_int(&Style::margin_r, -1);
    out.margin_v = pick_int(&Style::margin_v, -1);
    if (out.margin_l < 0) out.margin_l = 40;
    if (out.margin_r < 0) out.margin_r = 40;
    if (out.margin_v < 0) out.margin_v = 40;
    out.outline = pick_real(&Style::outline);
    out.shadow = pick_real(&Style::shadow);
    return out;
}

} // namespace asspipe
