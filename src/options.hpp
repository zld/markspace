#pragma once

#include <string>
#include <vector>

namespace lutcheck {

// Fully validated command line state. `parse_options` fills this only after
// every value has passed range checking, so the rest of the program never has
// to re-validate.
struct Options {
  std::string command;
  std::string input;        // single input for render/demo
  std::string lut;          // explicit .cube path, optional for demo
  std::string output;       // optional rendered PPM path
  bool write_rendered = false;
  std::string out_dir;      // batch output directory
  std::string input_space = "rgb";
  std::string lut_space = "rgb";
  std::vector<std::string> inputs;  // batch inputs, in order
  bool generate_fixtures = false;
  bool json = false;
  bool help = false;
  int grid_size = 17;
  int threads = 4;
  int histogram_bins = 16;
  double threshold = 1e-6;
  bool path_scalar = false;
  bool path_parallel = false;
};

}  // namespace lutcheck
