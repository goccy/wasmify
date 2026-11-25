package clangast

import (
	"strings"
	"testing"

	"github.com/goccy/wasmify/internal/apispec"
)

// leadingColonAST is clang's -ast-dump=json of a protoc-style header,
// which spells every type it names from the global scope:
//
//	namespace mylib {
//	enum Warning_Kind : int { Warning_Kind_A = 0 };
//	class Warning {
//	 public:
//	  ::mylib::Warning_Kind kind() const;
//	  ::int32_t line() const;
//	  ::int64_t offset() const;
//	  ::uint32_t flags() const;
//	  ::Global global() const;
//	  void set_line(::int32_t value);
//	};
//	}
//
// A function type keeps the spelling as written and has no desugared form.
const leadingColonAST = `{
	"kind": "TranslationUnitDecl",
	"inner": [
		{
			"kind": "NamespaceDecl",
			"name": "mylib",
			"loc": {"file": "/src/msg.pb.h"},
			"inner": [
				{
					"kind": "EnumDecl",
					"name": "Warning_Kind",
					"loc": {"file": "/src/msg.pb.h"},
					"inner": [
						{"kind": "EnumConstantDecl", "name": "Warning_Kind_A", "inner": [
							{"kind": "ConstantExpr", "value": "0"}
						]}
					]
				},
				{
					"kind": "CXXRecordDecl",
					"name": "Warning",
					"loc": {"file": "/src/msg.pb.h"},
					"tagUsed": "class",
					"definitionData": {},
					"inner": [
						{"kind": "AccessSpecDecl", "access": "public"},
						{"kind": "CXXMethodDecl", "name": "kind", "type": {"qualType": "::mylib::Warning_Kind () const"}},
						{"kind": "CXXMethodDecl", "name": "line", "type": {"qualType": "::int32_t () const"}},
						{"kind": "CXXMethodDecl", "name": "offset", "type": {"qualType": "::int64_t () const"}},
						{"kind": "CXXMethodDecl", "name": "flags", "type": {"qualType": "::uint32_t () const"}},
						{"kind": "CXXMethodDecl", "name": "global", "type": {"qualType": "::Global () const"}},
						{
							"kind": "CXXMethodDecl",
							"name": "set_line",
							"type": {"qualType": "void (::int32_t)"},
							"inner": [
								{"kind": "ParmVarDecl", "name": "value", "type": {"desugaredQualType": "int", "qualType": "::int32_t"}}
							]
						}
					]
				}
			]
		}
	]
}`

// parseLeadingColon runs the parse-headers flow on leadingColonAST: the
// per-batch parse, then the post-passes over the merged spec.
func parseLeadingColon(t *testing.T) map[string]apispec.Function {
	t.Helper()
	spec, err := ParseStreamMulti(strings.NewReader(leadingColonAST), []string{"/src/msg.pb.h"})
	if err != nil {
		t.Fatalf("ParseStreamMulti: %v", err)
	}
	PostProcessEnumTypes(spec)
	PostProcessHandleClasses(spec)
	PostProcessQualifyShortNames(spec)
	for _, c := range spec.Classes {
		if c.QualName != "mylib::Warning" {
			continue
		}
		methods := make(map[string]apispec.Function, len(c.Methods))
		for _, m := range c.Methods {
			methods[m.Name] = m
		}
		return methods
	}
	t.Fatalf("no mylib::Warning in %+v", spec.Classes)
	return nil
}

func TestParseStream_LeadingColonEnum(t *testing.T) {
	got := parseLeadingColon(t)["kind"].ReturnType
	if got.Kind != apispec.TypeEnum || got.Name != "mylib::Warning_Kind" {
		t.Errorf("kind() returns %+v, want enum mylib::Warning_Kind", got)
	}
}

func TestParseStream_LeadingColonBuiltinAlias(t *testing.T) {
	methods := parseLeadingColon(t)
	for method, want := range map[string]string{
		"line":   "int32_t",
		"offset": "int64_t",
		"flags":  "uint32_t",
	} {
		got := methods[method].ReturnType
		if got.Kind != apispec.TypePrimitive || got.Name != want {
			t.Errorf("%s() returns %+v, want primitive %s", method, got, want)
		}
		if strings.Contains(got.QualType, "mylib") {
			t.Errorf("%s() return qual_type %q is qualified with the enclosing namespace", method, got.QualType)
		}
	}
	// The desugared `int` also classifies as a primitive, so the name is
	// what shows the sugared `::int32_t` was recognised.
	param := methods["set_line"].Params[0].Type
	if param.Kind != apispec.TypePrimitive || param.Name != "int32_t" {
		t.Errorf("set_line(value) is %+v, want primitive int32_t", param)
	}
}

func TestParseStream_LeadingColonGlobalClassNotRequalified(t *testing.T) {
	got := parseLeadingColon(t)["global"].ReturnType
	// ::Global names the global-scope class; prepending the enclosing
	// namespace would name a class that does not exist, and dropping the
	// `::` would let a short-name lookup find mylib's own Global.
	if got.Name != "::Global" || strings.Contains(got.QualType, "mylib") {
		t.Errorf("global() returns %+v, want Global from the global scope", got)
	}
}
