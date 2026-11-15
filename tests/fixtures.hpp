#pragma once
#include <string>

namespace assaudit {

// Main demo document: clean structure, 8 styles, 6 inheritance edges,
// 12 dialogue events, 47 inline tags of which 2 are unknown.
std::string generateDemoAss();

// Boundary fixtures exercising error recovery; used by integration tests.
std::string generateMissingParentAss();
std::string generateCycleAss();
std::string generateDuplicateStyleAss();
std::string generateTimecodeBadAss();
std::string generateUnclosedTagAss();
std::string generateCommasAndEmptyAss();

}  // namespace assaudit
