#include "json.h"

#include <fstream>
#include <iostream>
#include <sstream>

int main(int argc, char** argv) {
    if (argc != 2) {
        std::cerr << "usage: subtitle-metrics FILE\n";
        return 2;
    }
    std::ifstream in(argv[1], std::ios::binary);
    std::ostringstream buffer;
    buffer << in.rdbuf();
    JsonValue root;
    std::string error;
    if (!parse_json(buffer.str(), root, error)) {
        std::cerr << "invalid JSON: " << error << "\n";
        return 2;
    }
    std::cout << "json ok\n";
    return 0;
}
