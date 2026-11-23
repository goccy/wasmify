package protogen

import (
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
)

// A trampoline answers a callback that unwound (callback_invoke returning
// -1) before it decodes an answer: it returns at once, reading and freeing
// nothing, with the configured error for an error-only result, a
// value-initialized result of another kind, nothing for a void one, and a
// trap for a reference one.
func TestTrampolineUnwound(t *testing.T) {
	prevCfg := bridgeConfig
	bridgeConfig = DefaultBridgeConfig()
	bridgeConfig.ErrorOnlyReturnTypes = []string{"absl::Status"}
	bridgeConfig.ErrorReconstruct["absl::Status"] = ErrorReturnSpec{
		ErrorExpr: "absl::InternalError({err_msg})",
		OkExpr:    "absl::OkStatus()",
	}
	defer func() { bridgeConfig = prevCfg }()

	intType := apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int", QualType: "int"}
	for _, tt := range []struct {
		name string
		ret  apispec.TypeRef
		want string
	}{
		{"value", intType, "return {};"},
		{"error-only", apispec.TypeRef{Kind: apispec.TypeValue, Name: "absl::Status", QualType: "absl::Status"},
			`return absl::InternalError(std::string("callback unwound"));`},
		{"void", apispec.TypeRef{Kind: apispec.TypeVoid, Name: "void", QualType: "void"}, "return;"},
		{"reference", apispec.TypeRef{Kind: apispec.TypePrimitive, Name: "int", QualType: "int &", IsRef: true}, "__builtin_trap();"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			writeTrampolineMethod(&b, &apispec.Function{
				Name:       "next",
				IsVirtual:  true,
				Params:     []apispec.Param{{Name: "step", Type: intType}},
				ReturnType: tt.ret,
			}, 0)
			out := b.String()
			guard := "if (_rc == -1) {\n            " + tt.want + "\n        }"
			gi := strings.Index(out, guard)
			if gi < 0 {
				t.Fatalf("no unwound guard %q in:\n%s", guard, out)
			}
			for _, after := range []string{"decode_result(_rc", "wasm_free("} {
				if i := strings.Index(out, after); i >= 0 && i < gi {
					t.Errorf("%q comes before the unwound guard:\n%s", after, out)
				}
			}
			if ci := strings.Index(out, "wasmify_callback_invoke("); ci < 0 || ci > gi {
				t.Errorf("the unwound guard is not after the call:\n%s", out)
			}
		})
	}
}
