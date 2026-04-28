// Stub emscripten/version.h for wasmify bridge compilation.
// This file exists so that abseil's <absl/base/config.h> can find it
// when __EMSCRIPTEN__ is defined. It does not actually provide emscripten
// functionality - it's just to satisfy the include check.
#ifndef EMSCRIPTEN_VERSION_H
#define EMSCRIPTEN_VERSION_H

#define __EMSCRIPTEN_major__ 0
#define __EMSCRIPTEN_minor__ 0
#define __EMSCRIPTEN_tiny__ 0

#endif
