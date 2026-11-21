#include "manifest.h"

#include "json.h"

#include <cerrno>
#include <cmath>
#include <fstream>
#include <set>
#include <sstream>

namespace {

constexpr std::size_t kMaxSubtitles = 10000;
constexpr std::size_t kMaxTextBytes = 16 * 1024;

void add_error(std::vector<ManifestError>& errors, const std::string& path,
               const std::string& message, int item_index = -1,
               const std::string& item_id = "") {
    ManifestError error;
    error.path = path;
    error.message = message;
    error.item_index = item_index;
    error.item_id = item_id;
    errors.push_back(std::move(error));
}

bool read_file(const std::string& path, std::string& out) {
    std::ifstream in(path, std::ios::binary);
    if (!in) {
        return false;
    }
    std::ostringstream buffer;
    buffer << in.rdbuf();
    if (!in.good() && !in.eof()) {
        return false;
    }
    out = buffer.str();
    return true;
}

bool require_int(const JsonValue& parent, const std::string& key,
                 const std::string& path, long min_value, long max_value,
                 long& out, std::vector<ManifestError>& errors) {
    const JsonValue* value = parent.find(key);
    if (value == nullptr) {
        add_error(errors, path + "." + key, "required field is missing");
        return false;
    }
    if (!value->is_number()) {
        add_error(errors, path + "." + key, "must be an integer");
        return false;
    }
    if (value->number != std::floor(value->number) ||
        value->number < static_cast<double>(min_value) ||
        value->number > static_cast<double>(max_value)) {
        add_error(errors, path + "." + key,
                  "must be an integer between " + std::to_string(min_value) +
                      " and " + std::to_string(max_value));
        return false;
    }
    out = static_cast<long>(value->number);
    return true;
}

bool require_string(const JsonValue& parent, const std::string& key,
                    const std::string& path, std::string& out,
                    std::vector<ManifestError>& errors) {
    const JsonValue* value = parent.find(key);
    if (value == nullptr) {
        add_error(errors, path + "." + key, "required field is missing");
        return false;
    }
    if (!value->is_string()) {
        add_error(errors, path + "." + key, "must be a string");
        return false;
    }
    out = value->string;
    return true;
}

bool parse_font(const JsonValue& root, Manifest& manifest,
                std::vector<ManifestError>& errors) {
    const JsonValue* font = root.find("font");
    if (font == nullptr || !font->is_object()) {
        add_error(errors, "$.font", "required object is missing or invalid");
        return false;
    }

    long number = 0;
    bool ok = true;
    ok &= require_string(*font, "path", "$.font", manifest.font.path, errors);
    if (ok) {
        std::ifstream probe(manifest.font.path, std::ios::binary);
        if (!probe) {
            add_error(errors, "$.font.path",
                      "font file does not exist or cannot be read: " +
                          manifest.font.path);
            ok = false;
        }
    }
    ok &= require_int(*font, "size", "$.font", 1, 512, number, errors);
    if (number != 0) {
        manifest.font.size = static_cast<int>(number);
    }
    manifest.font.letter_spacing = 0;
    if (font->find("letter_spacing") != nullptr) {
        number = 0;
        ok &= require_int(*font, "letter_spacing", "$.font", -64, 256, number,
                          errors);
        manifest.font.letter_spacing = static_cast<int>(number);
    }

    manifest.font.line_spacing = 1.0;
    const JsonValue* spacing = font->find("line_spacing");
    if (spacing != nullptr) {
        if (!spacing->is_number() || spacing->number < 0.0 ||
            spacing->number > 8.0) {
            add_error(errors, "$.font.line_spacing",
                      "must be a number between 0 and 8");
            ok = false;
        } else {
            manifest.font.line_spacing = spacing->number;
        }
    }

    std::string missing;
    if (font->find("missing_glyph") != nullptr) {
        if (!require_string(*font, "missing_glyph", "$.font", missing, errors)) {
            ok = false;
        } else if (missing != "error" && missing != "fallback") {
            add_error(errors, "$.font.missing_glyph",
                      "must be \"error\" or \"fallback\"");
            ok = false;
        } else {
            manifest.font.missing_glyph = missing;
        }
    } else {
        manifest.font.missing_glyph = "error";
    }
    return ok;
}

bool parse_canvas(const JsonValue& root, Manifest& manifest,
                  std::vector<ManifestError>& errors) {
    const JsonValue* canvas = root.find("canvas");
    if (canvas == nullptr || !canvas->is_object()) {
        add_error(errors, "$.canvas", "required object is missing or invalid");
        return false;
    }
    long number = 0;
    bool ok = true;
    ok &= require_int(*canvas, "width", "$.canvas", 1, 1000000, number, errors);
    manifest.canvas.width = static_cast<int>(number);
    number = 0;
    ok &= require_int(*canvas, "safe_inset", "$.canvas", 0, 1000000, number,
                      errors);
    manifest.canvas.safe_inset = static_cast<int>(number);
    if (ok && static_cast<long>(manifest.canvas.width) -
                 2L * manifest.canvas.safe_inset <
              1L) {
        add_error(errors, "$.canvas",
                  "safe_inset leaves no usable width inside the canvas");
        ok = false;
    }
    return ok;
}

bool parse_subtitles(const JsonValue& root, Manifest& manifest,
                     std::vector<ManifestError>& errors) {
    const JsonValue* list = root.find("subtitles");
    if (list == nullptr || !list->is_array()) {
        add_error(errors, "$.subtitles", "required array is missing or invalid");
        return false;
    }
    if (list->array.size() > kMaxSubtitles) {
        add_error(errors, "$.subtitles",
                  "batch exceeds the 10000 subtitle limit");
        return false;
    }
    std::set<std::string> ids;
    bool ok = true;
    for (std::size_t i = 0; i < list->array.size(); ++i) {
        const JsonValue& item = list->array[i];
        const std::string path = "$.subtitles[" + std::to_string(i) + "]";
        if (!item.is_object()) {
            add_error(errors, path, "subtitle entry must be an object",
                      static_cast<int>(i));
            ok = false;
            continue;
        }
        SubtitleItem subtitle;
        const bool has_id =
            require_string(item, "id", path, subtitle.id, errors);
        std::string id_for_error = has_id ? subtitle.id : std::string();
        const bool has_language =
            require_string(item, "language", path, subtitle.language, errors);
        const bool has_text =
            require_string(item, "text", path, subtitle.text, errors);
        if (!has_id || !has_language || !has_text) {
            ok = false;
            continue;
        }
        if (subtitle.id.empty()) {
            add_error(errors, path + ".id", "id must not be empty",
                      static_cast<int>(i));
            ok = false;
            continue;
        }
        if (!ids.insert(subtitle.id).second) {
            add_error(errors, path + ".id", "duplicate id \"" + subtitle.id + "\"",
                      static_cast<int>(i), subtitle.id);
            ok = false;
            continue;
        }
        if (subtitle.language.empty()) {
            add_error(errors, path + ".language", "language must not be empty",
                      static_cast<int>(i), subtitle.id);
            ok = false;
            continue;
        }
        if (subtitle.text.size() > kMaxTextBytes) {
            add_error(errors, path + ".text",
                      "text exceeds the 16 KiB per-subtitle limit",
                      static_cast<int>(i), subtitle.id);
            ok = false;
            continue;
        }
        manifest.items.push_back(std::move(subtitle));
    }
    return ok;
}

}  // namespace

bool load_manifest(const std::string& path, Manifest& manifest,
                   std::vector<ManifestError>& errors) {
    manifest = Manifest{};
    std::string text;
    if (!read_file(path, text)) {
        add_error(errors, "$", "cannot read manifest: " + path);
        return false;
    }
    JsonValue root;
    std::string parse_error;
    if (!parse_json(text, root, parse_error)) {
        add_error(errors, "$", "invalid JSON: " + parse_error);
        return false;
    }
    if (!root.is_object()) {
        add_error(errors, "$", "manifest root must be an object");
        return false;
    }

    bool ok = true;
    ok &= parse_font(root, manifest, errors);

    const JsonValue* canvas_root = root.find("canvas");
    if (canvas_root != nullptr) {
        ok &= parse_canvas(root, manifest, errors);
    } else {
        add_error(errors, "$.canvas", "required object is missing or invalid");
        ok = false;
    }

    long number = 0;
    const bool has_max_lines = require_int(root, "max_lines", "$", 1, 100,
                                           number, errors);
    if (has_max_lines) {
        manifest.max_lines = static_cast<int>(number);
    } else {
        ok = false;
    }

    ok &= parse_subtitles(root, manifest, errors);
    return ok;
}
