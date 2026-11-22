# The tag of C++ exceptions, which wasi-sdk's wasm64 libc++ (built with
# exception handling) refers to and does not define. A module normally
# imports it from its embedder; the test program defines it, so that it
# has no import but the host's.
	.tagtype	__cpp_exception i64
	.globl	__cpp_exception
__cpp_exception:
