#pragma once

#include <string>

namespace tl {

// The demo timeline text: 64x48 @ 24fps, four procedural clips joined by
// crossfade, wipe and radial transitions of 24 frames each.
std::string demo_manifest_text();

// Writes the demo manifest to path (via a temp file + rename).
// false + err on I/O failure.
bool write_demo(const std::string& path, std::string& err);

}  // namespace tl
