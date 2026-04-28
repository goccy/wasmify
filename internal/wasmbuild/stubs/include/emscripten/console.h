// Stub emscripten/console.h for wasi-sdk builds with -D__EMSCRIPTEN__.
// Abseil's raw_logging.cc includes this when __EMSCRIPTEN__ is defined.
#ifndef _WASMIFY_EMSCRIPTEN_CONSOLE_H
#define _WASMIFY_EMSCRIPTEN_CONSOLE_H

#include <stdio.h>

// Map Emscripten console functions to stdio
static inline void emscripten_console_log(const char* msg) {
    fputs(msg, stdout);
    fputc('\n', stdout);
}
static inline void emscripten_console_warn(const char* msg) {
    fputs(msg, stderr);
    fputc('\n', stderr);
}
static inline void emscripten_console_error(const char* msg) {
    fputs(msg, stderr);
    fputc('\n', stderr);
}

#endif
