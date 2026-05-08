package main

import (
	"testing"
)

// TestTransferWhenGoCondition pins the Go-expression renderer that
// drives the runtime-conditional ownership clear. Inputs are the
// JSON literal carried on `wasm_take_ownership_equals` plus the
// camelCase Go variable name of the selector parameter; output is
// the boolean expression embedded into:
//
//	if <expr> { clearPtrAny(<handle>) }
//
// Three primitive selector types are supported (matching
// internal/protogen/proto.go::encodeTransferWhenEquals): bool, int,
// string. bool collapses into idiomatic `if <var>` / `if !<var>`
// to avoid `== true` smell; int and string compare via `==` so the
// generated Go matches what a hand-written caller would write.
func TestTransferWhenGoCondition(t *testing.T) {
	cases := []struct {
		name     string
		selector string
		equals   string
		want     string
		wantErr  bool
	}{
		// bool selector
		{"bool-true", "isOwned", "true", "isOwned", false},
		{"bool-false", "isOwned", "false", "!isOwned", false},
		{"bool-true-camelcase", "transferOwnership", "true", "transferOwnership", false},

		// int selector
		{"int-1", "mode", "1", "mode == 1", false},
		{"int-zero", "mode", "0", "mode == 0", false},
		{"int-negative", "delta", "-7", "delta == -7", false},

		// string selector
		{"string-simple", "strategy", `"transfer"`, `strategy == "transfer"`, false},
		{"string-with-special-chars", "strategy", `"a\"b"`, `strategy == "a\"b"`, false},
		{"string-empty", "strategy", `""`, `strategy == ""`, false},

		// Errors
		{"missing-equals", "selector", "", "", true},
		{"unsupported-literal", "selector", "garbage", "", true},
		{"malformed-json-string", "selector", `"unterminated`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := transferWhenGoCondition(tc.selector, tc.equals)
			if (err != nil) != tc.wantErr {
				t.Fatalf("transferWhenGoCondition: err=%v, wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("transferWhenGoCondition(%q, %q) = %q; want %q",
					tc.selector, tc.equals, got, tc.want)
			}
		})
	}
}
