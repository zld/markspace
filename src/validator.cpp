#include "validator.hpp"

namespace mp4flow {

VerifyReport verifyMedia(const VerifyOptions& opt,
                         const std::vector<InitSignature>& inits) {
  VerifyReport rep;
  FragmentIndex idx = buildIndex(opt.mediaDir, inits);
  rep.fragmentFiles = idx.files.size();
  for (const FragmentFile& f : idx.files) rep.moofCount += f.moofs.size();
  rep.duplicates = idx.conflicts;
  rep.ordinalConflicts = idx.ordinalConflicts;

  // Sample layout must be fully self-consistent; throws Mp4Error on failure.
  validateSampleRanges(idx);
  rep.sampleRangesValid = true;

  if (opt.hasExpectedRange)
    rep.missing = idx.missingSequences(opt.expectFirst, opt.expectLast);

  rep.ok = rep.sampleRangesValid && rep.missing.empty() &&
           rep.ordinalConflicts.empty();
  return rep;
}

} // namespace mp4flow
