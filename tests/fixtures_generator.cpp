#include <iostream>

#include "fixtures.hpp"

int main() {
  // Smoke-exercise every generator; contents are validated by the
  // integration test, this executable also serves as a CTest target.
  std::cout << assaudit::generateDemoAss().size() << ' '
            << assaudit::generateMissingParentAss().size() << ' '
            << assaudit::generateCycleAss().size() << ' '
            << assaudit::generateDuplicateStyleAss().size() << ' '
            << assaudit::generateTimecodeBadAss().size() << ' '
            << assaudit::generateUnclosedTagAss().size() << ' '
            << assaudit::generateCommasAndEmptyAss().size() << '\n';
  return 0;
}
