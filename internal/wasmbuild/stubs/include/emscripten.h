// Stub emscripten.h for wasi-sdk builds with -D__EMSCRIPTEN__.
// Provides minimal declarations so abseil and other libraries that
// check __EMSCRIPTEN__ compile without the full Emscripten SDK.
#ifndef _WASMIFY_EMSCRIPTEN_H
#define _WASMIFY_EMSCRIPTEN_H

// emscripten_get_now() — used by abseil for timing
static inline double emscripten_get_now(void) { return 0.0; }

#endif
