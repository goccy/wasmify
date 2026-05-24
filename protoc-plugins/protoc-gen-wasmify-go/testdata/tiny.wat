;; Minimal module for exercising transpileGenwasm without a real
;; wasmify project. One exported function, one data segment.
(module
  (memory 1)
  (data (i32.const 0) "hi")
  (func (export "run") (param i32) (result i32)
    local.get 0
    i32.const 42
    i32.add))
