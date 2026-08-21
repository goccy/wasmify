#!/bin/bash
# build_sysroot.sh — build a wasm64-wasip1 sysroot into an existing wasi-sdk.
#
# The official wasi-sdk ships wasm32 sysroots only, but its clang already
# emits wasm64 code. This script fills the gap with three stages, so that
# `clang --target=wasm64-wasip1` (and clang++ with -fwasm-exceptions)
# works out of the box against the SDK's own sysroot:
#
#   1. wasi-libc, built for wasm64-wasip1 from a pinned upstream commit
#      plus the wasmify patch set (musl arch/wasm64, LP64 WASI ABI —
#      every wasi_snapshot_preview1 import argument widened to pointer
#      width, which is the ABI the wasm2go runtime binds).
#   2. compiler-rt builtins for wasm64, into clang's per-target
#      resource directory.
#   3. libunwind + libc++abi + libc++ built WITH wasm exception handling
#      (-fwasm-exceptions), because wasm64 C++ has no upstream runtime
#      at all and real-world C++ code throws.
#
# Idempotent: each stage stamps a .tag with its input pin and is skipped
# when the stamp matches. Host requirements: cmake, ninja, python3, curl.
#
# Env in:
#   WASI_SDK_PATH   (required) wasi-sdk installation to install into
#   PATCH_DIR       (required) directory holding the wasi-libc wasm64 patches
#   WASM64_WORK     workspace (default ${XDG_CACHE_HOME:-~/.cache}/wasmify/wasm64)
#   WASM64_THREADS  1 builds the wasm64-wasip1-threads flavor: wasi-libc with
#                   its posix thread model (real pthreads over
#                   wasi_thread_spawn), and every archive compiled with
#                   -pthread so its objects carry the atomics/bulk-memory
#                   features a --shared-memory link demands.
#   WASI_LIBC_COMMIT / LLVM_VERSION  override the pins (testing only)
set -euo pipefail

: "${WASI_SDK_PATH:?WASI_SDK_PATH is required}"
: "${PATCH_DIR:?PATCH_DIR is required}"
: "${WASM64_WORK:=${XDG_CACHE_HOME:-$HOME/.cache}/wasmify/wasm64}"
: "${WASM64_THREADS:=0}"

# wasi-libc pin: last verified against the wasm2go widened-ABI runtime.
: "${WASI_LIBC_COMMIT:=8d8348ec24253d0638a693b8af82445c13d92d32}"

if [ -z "${LLVM_VERSION:-}" ]; then
  # wasi-sdk's VERSION file carries `llvm-version: 22.1.0`; the runtimes
  # must come from the same release as the SDK's clang.
  LLVM_VERSION="$(sed -n 's/^llvm-version:[[:space:]]*//p' "$WASI_SDK_PATH/VERSION" | head -1)"
fi
if [ -z "$LLVM_VERSION" ]; then
  echo "cannot determine the LLVM version from $WASI_SDK_PATH/VERSION" >&2
  exit 1
fi

TRIPLE=wasm64-wasip1
THREAD_CFLAGS=""
if [ "$WASM64_THREADS" = "1" ]; then
  TRIPLE=wasm64-wasip1-threads
  THREAD_CFLAGS="-pthread"
fi
SYSROOT="$WASI_SDK_PATH/share/wasi-sysroot"
CLANG="$WASI_SDK_PATH/bin/clang"
RESDIR="$("$CLANG" -print-resource-dir)"
mkdir -p "$WASM64_WORK"

fetch() { # url dest
  if [ ! -f "$2" ]; then
    curl -fSL --proto '=https' --tlsv1.2 -o "$2.part" "$1" && mv "$2.part" "$2"
  fi
}

# ── stage 1: wasi-libc ──────────────────────────────────────────────────
LIBC_SRC="$WASM64_WORK/wasi-libc-$WASI_LIBC_COMMIT"
LIBC_TAG="$SYSROOT/lib/$TRIPLE/.wasmify-libc-tag"
if [ -f "$LIBC_TAG" ] && [ "$(cat "$LIBC_TAG")" = "$WASI_LIBC_COMMIT" ]; then
  echo "== wasm64 wasi-libc already installed ($WASI_LIBC_COMMIT)"
else
  echo "== building wasi-libc for $TRIPLE ($WASI_LIBC_COMMIT)"
  if [ ! -d "$LIBC_SRC" ]; then
    TARBALL="$WASM64_WORK/wasi-libc-$WASI_LIBC_COMMIT.tar.gz"
    fetch "https://github.com/WebAssembly/wasi-libc/archive/$WASI_LIBC_COMMIT.tar.gz" "$TARBALL"
    tar -xzf "$TARBALL" -C "$WASM64_WORK"
  fi
  # musl's wasm32 arch is pointer-width-agnostic (_Addr is `long`); the
  # wasm64 arch is a copy plus a pointer-width a_cas_p (patch 0005).
  rm -rf "$LIBC_SRC/libc-top-half/musl/arch/wasm64"
  cp -R "$LIBC_SRC/libc-top-half/musl/arch/wasm32" "$LIBC_SRC/libc-top-half/musl/arch/wasm64"
  for p in "$PATCH_DIR"/*.patch; do
    patch -p1 -d "$LIBC_SRC" -N -r - < "$p" >/dev/null || {
      # -N makes an already-applied patch non-fatal on re-runs of a
      # cached source tree; anything else is a real failure.
      patch -p1 -d "$LIBC_SRC" -R --dry-run < "$p" >/dev/null 2>&1 || {
        echo "patch failed: $p" >&2; exit 1; }
    }
  done
  BUILD="$LIBC_SRC/build-$TRIPLE"
  rm -rf "$BUILD"
  cmake -S "$LIBC_SRC" -B "$BUILD" -G Ninja \
    -DTARGET_TRIPLE="$TRIPLE" \
    -DCMAKE_C_COMPILER="$CLANG" \
    -DBUILD_SHARED=OFF -DBUILD_TESTS=OFF >/dev/null
  ninja -C "$BUILD" >/dev/null
  mkdir -p "$SYSROOT/lib/$TRIPLE" "$SYSROOT/include/$TRIPLE"
  cp -R "$BUILD/sysroot/lib/$TRIPLE/." "$SYSROOT/lib/$TRIPLE/"
  cp -R "$BUILD/sysroot/include/$TRIPLE/." "$SYSROOT/include/$TRIPLE/"
  echo "$WASI_LIBC_COMMIT" > "$LIBC_TAG"
  echo "== wasm64 wasi-libc installed"
fi

# ── shared LLVM source (compiler-rt + runtimes) ─────────────────────────
LLVM_SRC="$WASM64_WORK/llvm-project-$LLVM_VERSION.src"
need_llvm_src() {
  if [ ! -d "$LLVM_SRC/runtimes" ]; then
    TARBALL="$WASM64_WORK/llvm-project-$LLVM_VERSION.src.tar.xz"
    fetch "https://github.com/llvm/llvm-project/releases/download/llvmorg-$LLVM_VERSION/llvm-project-$LLVM_VERSION.src.tar.xz" "$TARBALL"
    # The pieces the two builds reach for; a full extract is >2 GiB.
    tar -xf "$TARBALL" -C "$WASM64_WORK" \
      "llvm-project-$LLVM_VERSION.src/compiler-rt" \
      "llvm-project-$LLVM_VERSION.src/runtimes" \
      "llvm-project-$LLVM_VERSION.src/libcxx" \
      "llvm-project-$LLVM_VERSION.src/libcxxabi" \
      "llvm-project-$LLVM_VERSION.src/libunwind" \
      "llvm-project-$LLVM_VERSION.src/libc" \
      "llvm-project-$LLVM_VERSION.src/cmake" \
      "llvm-project-$LLVM_VERSION.src/llvm/cmake" \
      "llvm-project-$LLVM_VERSION.src/llvm/utils/llvm-lit"
  fi
}

# ── stage 2: compiler-rt builtins ───────────────────────────────────────
RT_DIR="$RESDIR/lib/wasm64-unknown-wasip1"
if [ "$WASM64_THREADS" = "1" ]; then RT_DIR="$RESDIR/lib/wasm64-unknown-wasip1-threads"; fi
RT_TAG="$RT_DIR/.wasmify-tag"
if [ -f "$RT_TAG" ] && [ "$(cat "$RT_TAG")" = "$LLVM_VERSION" ]; then
  echo "== wasm64 compiler-rt builtins already installed ($LLVM_VERSION)"
else
  echo "== building compiler-rt builtins for $TRIPLE (LLVM $LLVM_VERSION)"
  need_llvm_src
  BUILD="$WASM64_WORK/build-rt-$TRIPLE"
  rm -rf "$BUILD"
  cmake -S "$LLVM_SRC/compiler-rt/lib/builtins" -B "$BUILD" -G Ninja \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_C_FLAGS="$THREAD_CFLAGS" \
    -DCMAKE_C_COMPILER="$CLANG" \
    -DCMAKE_C_COMPILER_TARGET="$TRIPLE" \
    -DCMAKE_ASM_COMPILER="$CLANG" \
    -DCMAKE_ASM_COMPILER_TARGET="$TRIPLE" \
    -DCMAKE_SYSROOT="$SYSROOT" \
    -DCMAKE_SYSTEM_NAME=WASI \
    -DCMAKE_SYSTEM_PROCESSOR=wasm64 \
    -DUNIX:BOOL=ON \
    -DLLVM_CMAKE_DIR="$LLVM_SRC/cmake" \
    -DCOMPILER_RT_DEFAULT_TARGET_ONLY=ON \
    -DCOMPILER_RT_BAREMETAL_BUILD=ON \
    -DCOMPILER_RT_HAS_FPIC_FLAG=OFF \
    -DCMAKE_C_COMPILER_WORKS=ON -DCMAKE_ASM_COMPILER_WORKS=ON \
    -DCMAKE_INSTALL_PREFIX="$BUILD/install" >/dev/null
  ninja -C "$BUILD" >/dev/null
  mkdir -p "$RT_DIR"
  # The builtins cmake names its output by target dir layout; take the
  # archive wherever it landed.
  RT_LIB="$(find "$BUILD" -name 'libclang_rt.builtins*.a' | head -1)"
  if [ -z "$RT_LIB" ]; then
    echo "compiler-rt build produced no builtins archive" >&2; exit 1
  fi
  cp "$RT_LIB" "$RT_DIR/libclang_rt.builtins.a"
  echo "$LLVM_VERSION" > "$RT_TAG"
  echo "== wasm64 compiler-rt builtins installed"
fi

# ── stage 3: EH-enabled C++ runtimes ────────────────────────────────────
CXX_TAG="$SYSROOT/lib/$TRIPLE/.wasmify-cxx-tag"
if [ -f "$CXX_TAG" ] && [ "$(cat "$CXX_TAG")" = "$LLVM_VERSION" ]; then
  echo "== wasm64 C++ runtimes already installed ($LLVM_VERSION)"
else
  echo "== building libunwind+libc++abi+libc++ for $TRIPLE with wasm EH (LLVM $LLVM_VERSION)"
  need_llvm_src
  # wasi-libc has no copy_file_range, but libcxx enables it for every
  # musl-like libc. Narrow the guard (idempotent).
  python3 - "$LLVM_SRC/libcxx/src/filesystem/operations.cpp" <<'PATCH'
import sys
path = sys.argv[1]
old = "#if _LIBCPP_GLIBC_PREREQ(2, 27) || _LIBCPP_HAS_MUSL_LIBC || defined(__FreeBSD__)"
new = ("#if (_LIBCPP_GLIBC_PREREQ(2, 27) || _LIBCPP_HAS_MUSL_LIBC || "
       "defined(__FreeBSD__)) && !defined(__wasi__)")
src = open(path).read()
if new in src:
    sys.exit(0)
if old not in src:
    sys.exit("copy_file_range guard not found in " + path)
open(path, "w").write(src.replace(old, new, 1))
PATCH
  BUILD="$WASM64_WORK/build-cxx-$TRIPLE"
  PREFIX="$WASM64_WORK/install-cxx-$TRIPLE"
  rm -rf "$BUILD" "$PREFIX"
  # Non-obvious options (shared with the wasm32 EH recipe):
  #   UNIX=ON       HandleLLVMOptions bails on the wasi triple otherwise.
  #   -fdeclspec    libunwind's config.h picks the __declspec branch on a
  #                 non-ELF target.
  #   ASM target    libunwind has .S sources; without an explicit ASM
  #                 target they assemble as wasm32 and poison the archive.
  #   THREADS ON    matches wasi-sdk's own libc++ (stub pthread).
  cmake -S "$LLVM_SRC/runtimes" -B "$BUILD" -G Ninja \
    -DCMAKE_SYSTEM_NAME=WASI \
    -DCMAKE_SYSTEM_PROCESSOR=wasm64 \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_C_COMPILER="$CLANG" \
    -DCMAKE_CXX_COMPILER="$WASI_SDK_PATH/bin/clang++" \
    -DCMAKE_ASM_COMPILER="$CLANG" \
    -DCMAKE_C_COMPILER_TARGET="$TRIPLE" \
    -DCMAKE_CXX_COMPILER_TARGET="$TRIPLE" \
    -DCMAKE_ASM_COMPILER_TARGET="$TRIPLE" \
    -DCMAKE_SYSROOT="$SYSROOT" \
    -DCMAKE_INSTALL_PREFIX="$PREFIX" \
    -DUNIX:BOOL=ON \
    -DLLVM_ENABLE_RUNTIMES="libunwind;libcxxabi;libcxx" \
    -DCMAKE_C_FLAGS="-fwasm-exceptions -fdeclspec $THREAD_CFLAGS" \
    -DCMAKE_CXX_FLAGS="-fwasm-exceptions -fdeclspec $THREAD_CFLAGS" \
    -DCMAKE_ASM_FLAGS="$THREAD_CFLAGS" \
    -DLIBCXX_ENABLE_EXCEPTIONS=ON -DLIBCXXABI_ENABLE_EXCEPTIONS=ON \
    -DLIBCXX_ENABLE_THREADS=ON -DLIBCXXABI_ENABLE_THREADS=ON -DLIBUNWIND_ENABLE_THREADS=ON \
    -DLIBCXX_HAS_PTHREAD_API=ON -DLIBCXXABI_HAS_PTHREAD_API=ON \
    -DLIBCXX_ENABLE_SHARED=OFF -DLIBCXXABI_ENABLE_SHARED=OFF -DLIBUNWIND_ENABLE_SHARED=OFF \
    -DLIBCXX_ENABLE_STATIC_ABI_LIBRARY=ON -DLIBCXX_CXX_ABI=libcxxabi \
    -DLIBCXX_ABI_VERSION=2 -DLIBCXX_HAS_MUSL_LIBC=ON \
    -DLIBCXXABI_USE_LLVM_UNWINDER=OFF \
    -DLIBCXX_INCLUDE_BENCHMARKS=OFF -DLIBCXX_INCLUDE_TESTS=OFF \
    -DCMAKE_C_COMPILER_WORKS=ON -DCMAKE_CXX_COMPILER_WORKS=ON >/dev/null
  ninja -C "$BUILD" >/dev/null
  ninja -C "$BUILD" install >/dev/null
  # The clang driver links C++ with -lc++ -lc++abi but never -lunwind,
  # and wasm EH needs libunwind's Unwind-wasm.c (__wasm_lpad_context,
  # _Unwind_CallPersonality, _Unwind_RaiseException). libc++abi is
  # already folded into libc++.a (LIBCXX_ENABLE_STATIC_ABI_LIBRARY);
  # fold libunwind in the same way so a bare `clang++ --target=wasm64-
  # wasip1 -fwasm-exceptions` link resolves.
  MERGE="$WASM64_WORK/merge-unwind"
  rm -rf "$MERGE" && mkdir -p "$MERGE"
  (cd "$MERGE" && "$WASI_SDK_PATH/bin/llvm-ar" x "$PREFIX/lib/libunwind.a" \
    && "$WASI_SDK_PATH/bin/llvm-ar" rs "$PREFIX/lib/libc++.a" ./*.o)
  # Per-triple layout the clang driver searches without extra flags:
  # headers at include/<triple>/c++/v1, archives at lib/<triple>.
  mkdir -p "$SYSROOT/include/$TRIPLE/c++"
  rm -rf "$SYSROOT/include/$TRIPLE/c++/v1"
  cp -R "$PREFIX/include/c++/v1" "$SYSROOT/include/$TRIPLE/c++/v1"
  cp "$PREFIX"/lib/*.a "$SYSROOT/lib/$TRIPLE/"
  echo "$LLVM_VERSION" > "$CXX_TAG"
  echo "== wasm64 C++ runtimes installed"
fi

echo "== wasm64 sysroot ready: --target=$TRIPLE --sysroot=$SYSROOT"
