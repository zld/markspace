#pragma once
//
// fixture_builder.hpp — Deterministic in-memory ASS sample generator.
// No external files are read; the fixture is fixed source code so every
// run produces byte-identical input.
//
#include <string>

namespace asspipe {

// Build the canonical demo fixture. It contains six styles (including two
// broken inheritance edges), twelve dialogue events exercising the full
// tag set, unclosed/empty/adjacent override blocks and one unknown tag.
std::string build_demo_ass();

} // namespace asspipe
