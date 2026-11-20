#ifndef GRADE_FIXTURE_HPP
#define GRADE_FIXTURE_HPP

#include <string>

#include "grade/cli.hpp"

namespace grade {

struct FixtureArtifacts {
    std::string inputPath;
    std::string lutPath;
    std::string patchesPath;
};

// Generates deterministic demo material inside opts.dir:
//   input.ppm   - sRGB horizontal/vertical/diagonal gradient (16-bit P6)
//   look.cube  - grid-sized grading LUT with a bounded, smooth look
//   patches.ppm - flat color blocks including LUT-domain boundary colors
// All content is derived solely from the options so reruns are identical.
// Throws UsageError on bad parameters and IoError on filesystem failures.
FixtureArtifacts generateFixtures(const FixtureOptions& opts);

} // namespace grade

#endif
