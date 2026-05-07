package clangast

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/goccy/wasmify/internal/apispec"
)

// Node represents a node in the clang AST JSON output.
type Node struct {
	ID                  string          `json:"id"`
	Kind                string          `json:"kind"`
	Name                string          `json:"name,omitempty"`
	QualType            string          `json:"qualType,omitempty"`
	MangledName         string          `json:"mangledName,omitempty"`
	Access              string          `json:"access,omitempty"`
	TagUsed             string          `json:"tagUsed,omitempty"`
	IsVirtual           bool            `json:"virtual,omitempty"`
	IsPure              bool            `json:"pure,omitempty"`
	StorageClass        string          `json:"storageClass,omitempty"`
	IsReferenced        bool            `json:"isReferenced,omitempty"`
	Inner               []Node          `json:"inner,omitempty"`
	Type                *Type           `json:"type,omitempty"`
	Bases               []Base          `json:"bases,omitempty"`
	Loc                 *Loc            `json:"loc,omitempty"`
	Range               *Range          `json:"range,omitempty"`
	IsImplicit          bool            `json:"isImplicit,omitempty"`
	DefinitionData      *DefData        `json:"definitionData,omitempty"`
	Value               json.RawMessage `json:"value,omitempty"`
	ScopedEnumTag       string          `json:"scopedEnumTag,omitempty"`
	FixedUnderlying     *Type           `json:"fixedUnderlyingType,omitempty"`
	// ExplicitlyDeleted is true for `= delete` methods/constructors.
	ExplicitlyDeleted bool `json:"explicitlyDeleted,omitempty"`
	// ParentDeclContextID identifies the enclosing declaration context (e.g., outer class)
	// for out-of-line nested class definitions like `class Outer::Nested final {...}`.
	// When present, the parent's qualified name should be used as the namespace.
	ParentDeclContextID string `json:"parentDeclContextId,omitempty"`
	// PreviousDecl points to a prior declaration of the same entity (e.g., the
	// in-class forward declaration for an out-of-line nested class definition).
	PreviousDecl string `json:"previousDecl,omitempty"`
	// Text carries the raw source text of a comment node (TextComment).
	// Only populated when Kind == "TextComment"; ignored otherwise.
	Text string `json:"text,omitempty"`
}

// Type holds type information from clang AST.
type Type struct {
	QualType     string `json:"qualType"`
	DesugaredQT  string `json:"desugaredQualType,omitempty"`
	TypeAlias    string `json:"typeAliasDeclId,omitempty"`
}

// Base represents a base class in CXXRecordDecl.
type Base struct {
	Access       string `json:"access"`
	Type         Type   `json:"type"`
	IsVirtual    bool   `json:"isVirtual,omitempty"`
	WrittenAccess string `json:"writtenAccess,omitempty"`
}

// Loc holds source location info.
type Loc struct {
	File         string `json:"file,omitempty"`
	Line         int    `json:"line,omitempty"`
	Col          int    `json:"col,omitempty"`
	IncludedFrom *struct {
		File string `json:"file,omitempty"`
	} `json:"includedFrom,omitempty"`
	SpellingLoc  *Loc   `json:"spellingLoc,omitempty"`
	ExpansionLoc *Loc   `json:"expansionLoc,omitempty"`
}

// Range holds source range info.
type Range struct {
	Begin Loc `json:"begin"`
	End   Loc `json:"end"`
}

// DefData holds class definition data.
type DefData struct {
	IsAbstract    bool `json:"isAbstract,omitempty"`
	IsPolymorphic bool `json:"isPolymorphic,omitempty"`
	// DefaultCtor describes the default constructor state.
	DefaultCtor *CtorInfo `json:"defaultCtor,omitempty"`
	// CopyCtor describes the copy constructor state.
	CopyCtor *CtorInfo `json:"copyCtor,omitempty"`
}

// CtorInfo describes a constructor from clang's definitionData output.
type CtorInfo struct {
	Exists              bool `json:"exists,omitempty"`
	NonTrivial          bool `json:"nonTrivial,omitempty"`
	Trivial             bool `json:"trivial,omitempty"`
	UserProvided        bool `json:"userProvided,omitempty"`
	UserDeclared        bool `json:"userDeclared,omitempty"`
	NeedsImplicit       bool `json:"needsImplicit,omitempty"`
	HasConstParam       bool `json:"hasConstParam,omitempty"`
	IsConstexpr         bool `json:"isConstexpr,omitempty"`
}

// DumpAST runs clang to get the AST JSON for the given header file.
// For small headers, this loads the entire AST into memory. For large
// headers, use DumpASTStream + ParseStream instead.
func DumpAST(clangPath string, headerFile string, flags []string) (*Node, error) {
	args := buildClangArgs(headerFile, flags)

	cmd := exec.Command(clangPath, args...)
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("clang AST dump failed: %w", err)
	}

	var root Node
	if err := json.Unmarshal(out, &root); err != nil {
		return nil, fmt.Errorf("failed to parse clang AST JSON: %w", err)
	}
	return &root, nil
}

// DumpASTStream runs clang and returns a streaming reader for the AST JSON.
// The caller must call the returned cleanup function when done.
func DumpASTStream(clangPath string, headerFile string, flags []string) (io.ReadCloser, func() error, error) {
	args := buildClangArgs(headerFile, flags)

	cmd := exec.Command(clangPath, args...)
	cmd.Stderr = os.Stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("clang AST dump failed to start: %w", err)
	}

	cleanup := func() error {
		return cmd.Wait()
	}

	return stdout, cleanup, nil
}

// BuildSyntaxCheckArgs constructs clang arguments for syntax-only validation
// (no AST dump). Used to pre-validate umbrella headers for conflicts before
// the full AST parse.
func BuildSyntaxCheckArgs(headerFile string, flags []string) []string {
	args := []string{
		"-fsyntax-only",
		"-w", // suppress warnings
	}

	ext := strings.ToLower(headerFile)
	if strings.HasSuffix(ext, ".h") || strings.HasSuffix(ext, ".hpp") || strings.HasSuffix(ext, ".hxx") {
		args = append(args, "-x", "c++")
	}

	if runtime.GOOS == "darwin" && !hasSysrootFlag(flags) {
		if sdkFlags := detectMacOSSDKFlags(); len(sdkFlags) > 0 {
			args = append(args, sdkFlags...)
		}
	}

	args = append(args, flags...)
	args = append(args, headerFile)
	return args
}

// buildClangArgs constructs the clang arguments for AST dumping.
func buildClangArgs(headerFile string, flags []string) []string {
	args := []string{
		"-Xclang", "-ast-dump=json",
		"-fsyntax-only",
		// Attach every comment (`//`, `/* */`, doc-style alike) to the
		// adjacent declaration so the parser can lift them onto the
		// generated apispec entries. Without this, only Doxygen-style
		// `///` and `/** */` comments would be preserved by clang.
		"-fparse-all-comments",
		"-w", // suppress warnings (we only need the AST)
	}

	// For .h files, force C++ mode so clang doesn't treat them as C
	ext := strings.ToLower(headerFile)
	if strings.HasSuffix(ext, ".h") || strings.HasSuffix(ext, ".hpp") || strings.HasSuffix(ext, ".hxx") {
		args = append(args, "-x", "c++")
	}

	// On macOS, auto-detect SDK sysroot if not already specified in flags
	if runtime.GOOS == "darwin" && !hasSysrootFlag(flags) {
		if sdkFlags := detectMacOSSDKFlags(); len(sdkFlags) > 0 {
			args = append(args, sdkFlags...)
		}
	}

	args = append(args, flags...)
	args = append(args, headerFile)
	return args
}

// Parser converts a clang AST tree into an APISpec.
type Parser struct {
	spec        *apispec.APISpec
	headerFile  string                    // single header (legacy, for backward compat)
	headerFiles map[string]bool           // set of target header file paths
	classes     map[string]*apispec.Class // qualName -> Class for dedup
	// allowedExternalClasses lists fully-qualified class names whose
	// declarations the parser keeps even when their source file is
	// outside the project header set. Populated from
	// `bridge.ExternalTypes` so callers no longer need a parallel
	// `IncludeExternalClasses` knob to expose the methods of types
	// already permitted in I/F signatures.
	allowedExternalClasses map[string]bool
	// classIDs maps clang AST declaration ID → fully qualified class name.
	// Used to resolve parentDeclContextId references for out-of-line nested
	// class definitions.
	classIDs map[string]string
	// classParentIDs records the clang ParentDeclContextID for each class
	// we parsed under a particular qualName. This lets us run a deferred
	// fixup pass after all classes have been visited, when the parent
	// class's ID has finally been registered. Without this, out-of-line
	// nested class definitions (e.g. `class Outer::Inner { ... }` placed
	// in a separate .inl header) can lose the `Outer::` prefix because
	// parseClass ran before Outer's ID was in classIDs.
	classParentIDs map[string]string // qualName -> ParentDeclContextID
	// classAliases records class-scoped type aliases such as
	//   class Foo { using Value = intptr_t; ... };
	// Keyed by class qualified name, inner map is alias -> underlying qualType.
	// Used to desugar method return/param types that reference a
	// class-scoped alias — the clang AST's sugared form ("Value") is
	// otherwise indistinguishable from a real class type.
	classAliases map[string]map[string]string
	// globalAliases records namespace/file-scope typedefs such as
	//   typedef std::vector<FunctionArgumentType> FunctionArgumentTypeList;
	// Keyed by the fully qualified alias name ("ns::Alias" or "Alias" for
	// the global scope). The value carries the underlying type spelling
	// and the namespace context — the latter is used to fully qualify
	// any unqualified class names that appear inside the underlying
	// spelling (clang prints names as-typed within the defining
	// namespace, leaving "Type" instead of "googlesql::Type").
	globalAliases map[string]aliasInfo
}

// aliasInfo records a typedef's underlying type spelling and the
// namespace it was defined in. The namespace lets us re-qualify
// unqualified class identifiers that appear inside the underlying
// spelling at expansion time.
type aliasInfo struct {
	Underlying string
	Namespace  string
}

// isIdentChar reports whether c may appear in a C++ identifier.
func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '_'
}

// extractComment lifts the doc comment attached to a clang AST decl
// node (FunctionDecl, CXXRecordDecl, FieldDecl, EnumDecl, …) into a
// plain string. clang represents an attached comment as a FullComment
// child whose own children are ParagraphComment nodes; each paragraph
// contains TextComment children whose `text` field carries one source
// line. Multiple paragraphs are joined by a blank line so the
// downstream Go-doc emitter can reproduce the original block structure.
// Returns the empty string when no comment is attached.
//
// `-fparse-all-comments` (added in buildClangArgs) is required for
// non-Doxygen-style comments to surface here.
func extractComment(node *Node) string {
	if node == nil {
		return ""
	}
	var paragraphs []string
	for i := range node.Inner {
		child := &node.Inner[i]
		if child.Kind != "FullComment" {
			continue
		}
		for j := range child.Inner {
			para := &child.Inner[j]
			if para.Kind != "ParagraphComment" {
				continue
			}
			var lines []string
			for k := range para.Inner {
				tc := &para.Inner[k]
				if tc.Kind != "TextComment" {
					continue
				}
				lines = append(lines, strings.TrimRight(tc.Text, " \t"))
			}
			if joined := strings.TrimRight(strings.Join(lines, "\n"), " \t\n"); joined != "" {
				paragraphs = append(paragraphs, joined)
			}
		}
	}
	return strings.TrimSpace(strings.Join(paragraphs, "\n\n"))
}

// newParser creates a Parser that matches against the given header files.
func newParser(headerFiles []string) *Parser {
	hf := make(map[string]bool, len(headerFiles))
	for _, f := range headerFiles {
		hf[f] = true
	}
	sourceFile := ""
	singleHeader := ""
	if len(headerFiles) == 1 {
		sourceFile = headerFiles[0]
		singleHeader = headerFiles[0]
	}
	return &Parser{
		spec:                   &apispec.APISpec{SourceFile: normalizeSourceFile(sourceFile)},
		headerFile:             singleHeader,
		headerFiles:            hf,
		classes:                make(map[string]*apispec.Class),
		classIDs:               make(map[string]string),
		classParentIDs:         make(map[string]string),
		classAliases:           make(map[string]map[string]string),
		globalAliases:          make(map[string]aliasInfo),
		allowedExternalClasses: make(map[string]bool),
	}
}

// SetAllowedExternalClasses lets the caller list fully-qualified
// class names that the parser should retain even when their
// declaration site is outside the project header set. The classes
// still have to appear in clang's AST (typically via a transitive
// `#include` from a project header); without this list the parser
// drops them at the matchesTarget filter.
func (p *Parser) SetAllowedExternalClasses(qualNames []string) {
	for _, q := range qualNames {
		p.allowedExternalClasses[q] = true
	}
}

// fixupNestedClassQualNames re-resolves parent context IDs for classes
// that were parsed before their parent's ID had been registered in
// classIDs. This repairs the qualName of out-of-line nested class
// definitions such as
//
//	// in value.h
//	class Value {
//	  class Metadata { class Content; ... };
//	};
//
//	// in value_inl.h
//	class Value::Metadata::Content { ... };
//
// When the parser walks value_inl.h, it sees Content as a top-level
// CXXRecordDecl with parentDeclContextId = Metadata's AST id. If that id
// wasn't yet in p.classIDs when parseClass ran, the class was registered
// as `ns::Content` (from the surrounding namespace) rather than
// `ns::Value::Metadata::Content`. This fixup repairs the qualName
// once all classes have been visited and classIDs is complete.
//
// Runs after the full walk completes; safe to call more than once.
func (p *Parser) fixupNestedClassQualNames() {
	if len(p.classParentIDs) == 0 {
		return
	}
	// Iterate until no more fixups happen, because a nested chain
	// (A::B::C::D) may require several passes as each parent is reached.
	for changed := true; changed; {
		changed = false
		for oldQual, parentID := range p.classParentIDs {
			parentQual, ok := p.classIDs[parentID]
			if !ok {
				continue
			}
			cls, exists := p.classes[oldQual]
			if !exists {
				// Already rewritten in a previous iteration.
				delete(p.classParentIDs, oldQual)
				continue
			}
			newQual := parentQual + "::" + cls.Name
			if newQual == oldQual {
				// Already correct, nothing to do.
				delete(p.classParentIDs, oldQual)
				continue
			}
			// Don't clobber a class already present under the new name.
			if _, collision := p.classes[newQual]; collision {
				delete(p.classParentIDs, oldQual)
				continue
			}
			cls.Namespace = parentQual
			cls.QualName = newQual
			delete(p.classes, oldQual)
			p.classes[newQual] = cls
			// classIDs pointed at oldQual; update so any further
			// children keyed on this class resolve the new name.
			for id, q := range p.classIDs {
				if q == oldQual {
					p.classIDs[id] = newQual
				}
			}
			delete(p.classParentIDs, oldQual)
			changed = true
		}
	}
}

// qualNameAllowed reports whether the (namespace, name) tuple
// resolves to a fully-qualified class name listed in
// allowedExternalClasses. Used as the type-system-driven fallback
// path so bridge.ExternalTypes-listed classes survive
// matchesTarget filtering even when declared in non-project
// headers.
func (p *Parser) qualNameAllowed(namespace, name string) bool {
	if name == "" || len(p.allowedExternalClasses) == 0 {
		return false
	}
	if p.allowedExternalClasses[name] {
		return true
	}
	if namespace != "" && p.allowedExternalClasses[namespace+"::"+name] {
		return true
	}
	return false
}

// matchesTarget checks if a file path matches any of the target header files.
func (p *Parser) matchesTarget(file string) bool {
	if p.headerFile != "" {
		return matchesHeaderFile(file, p.headerFile)
	}
	// Check against all target headers
	for hf := range p.headerFiles {
		if matchesHeaderFile(file, hf) {
			return true
		}
	}
	return false
}

// Parse extracts API information from the clang AST root node (in-memory).
// Only declarations from the given headerFile (or its directly included files) are considered.
func Parse(root *Node, headerFile string) *apispec.APISpec {
	p := newParser([]string{headerFile})
	p.walk(root, "", false, "")
	// Re-resolve qual names for classes whose parent class was visited
	// AFTER the class itself — see fixupNestedClassQualNames for why.
	p.fixupNestedClassQualNames()
	// Expand namespace-scope typedefs now that the full alias map is
	// known — see postProcessTypedefAliases for the motivation.
	p.postProcessTypedefAliases()
	// Resolve unqualified type references against the full namespace /
	// class scope universe before downstream code looks them up by
	// FQDN — see postProcessQualifyShortNames for the rationale.
	p.postProcessQualifyShortNames()
	// Flatten classes map
	for _, c := range p.classes {
		p.spec.Classes = append(p.spec.Classes, *c)
	}
	// Single-file parse (no multi-batch merging), so short-name promotion
	// is safe — there's no risk of a later batch invalidating the rewrite.
	postProcessEnumTypes(p.spec, true)
	postProcessHandleClasses(p.spec)
	return p.spec
}

// PostProcessEnumTypes is the exported entry point to re-run enum
// reclassification on a merged APISpec. It is safe to call multiple times
// and is idempotent. Callers that stitch together multiple per-batch specs
// (e.g., the umbrella-header parse-headers flow) should invoke this after
// merging so that enum refs defined in one batch but referenced from
// another are properly classified. Short-name heuristics ARE enabled in
// this path since the merged spec has full visibility into all enums and
// all classes.
func PostProcessEnumTypes(spec *apispec.APISpec) {
	postProcessEnumTypes(spec, true)
}

// PostProcessHandleClasses is the exported entry point to reclassify
// field-less classes as handle types. See postProcessHandleClasses for
// details. Idempotent and safe to call multiple times.
func PostProcessHandleClasses(spec *apispec.APISpec) {
	postProcessHandleClasses(spec)
}

// postProcessTypedefAliases expands namespace-/file-scope typedef
// aliases that were collected during the streaming walk. Because the
// streaming parser processes declarations top-to-bottom, a function
// whose signature references an alias (e.g.
// `FunctionArgumentTypeList`) may be emitted before the typedef itself
// is seen. This pass re-classifies every TypeRef whose Name matches a
// known alias, substituting the underlying type so downstream stages
// (proto gen, bridge gen) see the real std::vector<...> shape.
//
// Identifiers in the underlying spelling that clang left unqualified
// (typedefs in `namespace googlesql { typedef vector<Type*> ...; }`
// print "Type", not "googlesql::Type") are re-qualified against the
// project's class map and the typedef's defining namespace so
// downstream type lookups land on the real qualified class.
// postProcessQualifyShortNames rewrites parameter and return type
// `qualType` strings whose bare class name is not yet
// fully-qualified, using C++ unqualified-name lookup against the
// namespaces and class scopes already discovered during the
// stream parse.
//
// clang's `-ast-dump=json` preserves the source spelling of types,
// so a parameter declared inside `namespace google::protobuf`
// as `SourceLocation*` lands here with `qualType =
// "SourceLocation *"`. Downstream code that maps short names to
// FQDNs via classQualNames picks whichever entry happened to land
// in the map -- which can resolve `SourceLocation` to
// `googlesql_base::SourceLocation` even though
// `Descriptor::GetSourceLocation`'s parameter is in
// `google::protobuf` scope.
//
// This pass closes that gap by approximating the C++
// unqualified-name lookup rules at parse time:
//
//   1. Class scope first -- nested types of the enclosing class.
//   2. Inside-out walk of the enclosing namespace stack.
//   3. Global (no-prefix) scope.
//
// The first hit wins. The resolved FQDN is written back to both
// `Name` (so downstream lookups by `Name` match) and `QualType`
// (so any string-based bridge emit picks up the qualified form).
func (p *Parser) postProcessQualifyShortNames() {
	// Build the universe of known qualified names (classes + enums).
	known := make(map[string]bool, len(p.classes)+len(p.spec.Enums))
	parents := make(map[string][]string, len(p.classes))
	for q, c := range p.classes {
		known[q] = true
		if len(c.Parents) > 0 {
			parents[q] = c.Parents
		}
	}
	for _, e := range p.spec.Enums {
		if e.QualName != "" {
			known[e.QualName] = true
		}
	}

	// Free functions: no enclosing class context, just namespace.
	for i := range p.spec.Functions {
		fn := &p.spec.Functions[i]
		ns := fn.Namespace
		qualifyTypeRef(&fn.ReturnType, ns, "", known, parents)
		for j := range fn.Params {
			qualifyTypeRef(&fn.Params[j].Type, ns, "", known, parents)
		}
	}

	// Class methods: enclosing class scope + its namespace.
	for _, c := range p.classes {
		ns := c.Namespace
		classQual := c.QualName
		for i := range c.Methods {
			m := &c.Methods[i]
			qualifyTypeRef(&m.ReturnType, ns, classQual, known, parents)
			for j := range m.Params {
				qualifyTypeRef(&m.Params[j].Type, ns, classQual, known, parents)
			}
		}
		for i := range c.Fields {
			qualifyTypeRef(&c.Fields[i].Type, ns, classQual, known, parents)
		}
	}
}

// PostProcessQualifyShortNames is the exported entry point that
// re-runs unqualified-name resolution on a merged APISpec. The
// per-batch pass in ParseStream / ParseStreamMultiWithOptions can
// only see the classes that landed in its own batch; cross-batch
// references (e.g. a method declared in batch A whose parameter
// type is defined in batch B) need a second pass with the full
// merged class / enum universe in view. Idempotent.
func PostProcessQualifyShortNames(spec *apispec.APISpec) {
	if spec == nil {
		return
	}
	known := make(map[string]bool, len(spec.Classes)+len(spec.Enums))
	parents := make(map[string][]string, len(spec.Classes))
	for i := range spec.Classes {
		c := &spec.Classes[i]
		if c.QualName != "" {
			known[c.QualName] = true
			if len(c.Parents) > 0 {
				parents[c.QualName] = c.Parents
			}
		}
	}
	for i := range spec.Enums {
		if q := spec.Enums[i].QualName; q != "" {
			known[q] = true
		}
	}
	for i := range spec.Functions {
		fn := &spec.Functions[i]
		ns := fn.Namespace
		qualifyTypeRef(&fn.ReturnType, ns, "", known, parents)
		for j := range fn.Params {
			qualifyTypeRef(&fn.Params[j].Type, ns, "", known, parents)
		}
	}
	for i := range spec.Classes {
		c := &spec.Classes[i]
		ns := c.Namespace
		classQual := c.QualName
		for j := range c.Methods {
			m := &c.Methods[j]
			qualifyTypeRef(&m.ReturnType, ns, classQual, known, parents)
			for k := range m.Params {
				qualifyTypeRef(&m.Params[k].Type, ns, classQual, known, parents)
			}
		}
		for j := range c.Fields {
			qualifyTypeRef(&c.Fields[j].Type, ns, classQual, known, parents)
		}
	}
}

// qualifyTypeRef rewrites ref.QualType / ref.Name to use the
// fully-qualified class name when the as-written spelling is
// unqualified. Recurses into ref.Inner (template args /
// container element types).
func qualifyTypeRef(ref *apispec.TypeRef, ns, classQual string, known map[string]bool, parents map[string][]string) {
	if ref == nil {
		return
	}
	if ref.Inner != nil {
		qualifyTypeRef(ref.Inner, ns, classQual, known, parents)
	}
	if ref.Kind != apispec.TypeHandle && ref.Kind != apispec.TypeValue {
		return
	}
	bare := bareTypeName(ref.QualType)
	if bare == "" || strings.Contains(bare, "::") {
		return
	}
	fqn := lookupUnqualified(bare, ns, classQual, known, parents)
	if fqn == "" {
		// Fallback for type references whose declaration is not in
		// any parsed batch (typically external library types like
		// `google::protobuf::SourceLocation` reachable from a
		// project-public method but never parsed because the
		// header lives outside the scan set). C++ unqualified-name
		// lookup in the absence of a `using namespace` directive
		// always falls back to the enclosing namespace, so prepend
		// it so downstream code does not have to disambiguate.
		// Empty namespace (free / extern "C" / global scope) is
		// left alone.
		if ns == "" {
			return
		}
		fqn = ns + "::" + bare
	}
	if fqn == bare {
		return
	}
	ref.Name = fqn
	// Replace only the first occurrence of the bare name in
	// QualType so the cv-qualifiers and pointer/reference markers
	// surrounding it survive intact.
	ref.QualType = replaceBareIdent(ref.QualType, bare, fqn)
}

// lookupUnqualified mirrors C++ unqualified-name lookup at the
// declaration site of a method or free function. classQual may
// be empty (free function); ns is the enclosing namespace.
// parents maps each class's qual_name to its base classes' qual_names
// so the lookup can resolve nested types inherited from a base.
func lookupUnqualified(name, ns, classQual string, known map[string]bool, parents map[string][]string) string {
	// 1. Class scope: nested types of the enclosing class, walking
	//    the inheritance chain. C++ unqualified-name lookup inside
	//    a member function searches the class itself, then its base
	//    classes (depth-first), then the enclosing namespace -- so
	//    a derived class referencing `Status` resolves to
	//    `Base::Status` if the derived class doesn't redeclare it.
	if classQual != "" {
		visited := map[string]bool{}
		var walk func(cls string) string
		walk = func(cls string) string {
			if visited[cls] {
				return ""
			}
			visited[cls] = true
			candidate := cls + "::" + name
			if known[candidate] {
				return candidate
			}
			for _, parent := range parents[cls] {
				if got := walk(parent); got != "" {
					return got
				}
			}
			return ""
		}
		if got := walk(classQual); got != "" {
			return got
		}
	}
	// 2. Inside-out walk of the namespace stack.
	if ns != "" {
		segments := strings.Split(ns, "::")
		for i := len(segments); i > 0; i-- {
			scope := strings.Join(segments[:i], "::")
			candidate := scope + "::" + name
			if known[candidate] {
				return candidate
			}
		}
	}
	// 3. Global scope.
	if known[name] {
		return name
	}
	return ""
}

// bareTypeName strips cv qualifiers, pointer/reference markers,
// and elaborated-type-specifier keywords from a clang qualType
// string and returns the bare identifier. Returns "" when the
// remainder is not a single identifier (e.g. template
// instantiations -- those keep their existing form because
// qualification of template arguments would land on
// ref.Inner).
func bareTypeName(qt string) string {
	s := strings.TrimSpace(qt)
	for {
		prev := s
		s = strings.TrimSpace(s)
		s = strings.TrimSuffix(s, "*")
		s = strings.TrimSuffix(s, "&")
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "const ")
		s = strings.TrimPrefix(s, "volatile ")
		s = strings.TrimPrefix(s, "class ")
		s = strings.TrimPrefix(s, "struct ")
		s = strings.TrimPrefix(s, "union ")
		s = strings.TrimPrefix(s, "enum ")
		s = strings.TrimPrefix(s, "::")
		s = strings.TrimSpace(s)
		if s == prev {
			break
		}
	}
	if s == "" || strings.ContainsAny(s, "<>(),") || strings.Contains(s, " ") {
		return ""
	}
	return s
}

// replaceBareIdent replaces the first whole-identifier occurrence
// of `from` with `to` inside a qualType string. The simple
// strings.Replace would also match substrings of longer names
// (e.g. replacing `Type` inside `MyType`); whole-identifier
// matching guards by checking the surrounding characters.
func replaceBareIdent(qt, from, to string) string {
	idx := strings.Index(qt, from)
	if idx < 0 {
		return qt
	}
	endsWell := func(c byte) bool {
		return !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == ':')
	}
	startsWell := func(c byte) bool {
		// Allow `::` as a leading delimiter (for already-qualified
		// names) but treat the delimiter as ":" only if it is part
		// of the C++ scope operator.
		return !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9'))
	}
	for idx >= 0 {
		ok := true
		if idx > 0 && !startsWell(qt[idx-1]) {
			ok = false
		}
		end := idx + len(from)
		if ok && end < len(qt) && !endsWell(qt[end]) {
			ok = false
		}
		if ok {
			return qt[:idx] + to + qt[end:]
		}
		next := strings.Index(qt[idx+1:], from)
		if next < 0 {
			break
		}
		idx = idx + 1 + next
	}
	return qt
}

func (p *Parser) postProcessTypedefAliases() {
	if len(p.globalAliases) == 0 {
		return
	}
	// Short class name → unique fully-qualified name. Ambiguous short
	// names (multiple classes share one) are skipped to avoid picking
	// the wrong qualifier.
	shortToQual := make(map[string]string)
	shortConflict := make(map[string]bool)
	for qual := range p.classes {
		short := qual
		if idx := strings.LastIndex(qual, "::"); idx >= 0 {
			short = qual[idx+2:]
		}
		if existing, ok := shortToQual[short]; ok && existing != qual {
			shortConflict[short] = true
			continue
		}
		shortToQual[short] = qual
	}

	// qualifyIdentifier returns the FQ form of a single identifier,
	// preferring the typedef's own namespace if the class is defined
	// there, then falling back to a unique global match.
	qualifyIdentifier := func(name, ns string) string {
		if name == "" || strings.Contains(name, "::") {
			return name
		}
		if ns != "" {
			candidate := ns + "::" + name
			if _, ok := p.classes[candidate]; ok {
				return candidate
			}
		}
		if shortConflict[name] {
			return name
		}
		if q, ok := shortToQual[name]; ok {
			return q
		}
		return name
	}

	// qualifyTypeSpelling walks a C++ type spelling and re-qualifies
	// every identifier-shaped token that matches a known class. Tokens
	// are alphanumeric/underscore/`::` runs; everything else (template
	// brackets, pointer/ref qualifiers, `const`, whitespace) is copied
	// through verbatim.
	qualifyTypeSpelling := func(s, ns string) string {
		var out strings.Builder
		out.Grow(len(s))
		i := 0
		for i < len(s) {
			c := s[i]
			if isIdentChar(c) {
				j := i
				for j < len(s) {
					if isIdentChar(s[j]) || (s[j] == ':' && j+1 < len(s) && s[j+1] == ':') {
						if s[j] == ':' {
							j += 2
						} else {
							j++
						}
						continue
					}
					break
				}
				token := s[i:j]
				out.WriteString(qualifyIdentifier(token, ns))
				i = j
				continue
			}
			out.WriteByte(c)
			i++
		}
		return out.String()
	}

	var rewrite func(ref *apispec.TypeRef)
	rewrite = func(ref *apispec.TypeRef) {
		if ref == nil || ref.Name == "" {
			return
		}
		if info, ok := p.globalAliases[ref.Name]; ok && info.Underlying != ref.Name {
			under := qualifyTypeSpelling(info.Underlying, info.Namespace)
			inner := p.classifyType(under)
			// Preserve the outer cv / pointer / reference state.
			inner.IsConst = ref.IsConst || inner.IsConst
			inner.IsPointer = ref.IsPointer || inner.IsPointer
			inner.IsRef = ref.IsRef || inner.IsRef
			// Preserve the original qual_type so the C++ side keeps
			// whatever spelling clang saw in the source.
			if ref.QualType != "" {
				inner.QualType = ref.QualType
			}
			*ref = inner
		}
		if ref.Inner != nil {
			rewrite(ref.Inner)
		}
	}
	for i := range p.spec.Functions {
		fn := &p.spec.Functions[i]
		rewrite(&fn.ReturnType)
		for j := range fn.Params {
			rewrite(&fn.Params[j].Type)
		}
	}
	for _, c := range p.classes {
		for j := range c.Fields {
			rewrite(&c.Fields[j].Type)
		}
		for j := range c.Methods {
			m := &c.Methods[j]
			rewrite(&m.ReturnType)
			for k := range m.Params {
				rewrite(&m.Params[k].Type)
			}
		}
	}
}

// postProcessHandleClasses promotes encapsulated classes to handle types.
//
// Motivation: classifyType is purely syntactic — it classifies a by-value
// reference (T, not T* or T&) as TypeValue. That assumption is only sound
// for true POD types whose complete state is visible through public data
// members. Classes with no public fields at all
// (all state is private), and mixed classes like
//     class Foo { public: int id; private: std::vector<int> cache; };
// have SOME visible state but also hidden state that only methods can
// reach. For both kinds the bridge cannot "copy the fields"; it must
// keep the C++ object alive and pass a pointer across the bridge so that
// successive method calls operate on the same object and its private
// state is preserved.
//
// The rule:
//   - A class is promoted to a handle when it has at least one method
//     AND either (a) zero public fields, or (b) at least one non-public
//     (private or protected) field.
//   - Pure POD-style structs (all fields public, no hidden state) remain
//     as TypeValue even if they happen to have methods, since their
//     complete state IS serializable.
//   - Classes with neither fields nor methods are left alone — promoting
//     a forward-declared tag type to a handle would give callers an
//     opaque pointer with no API to use.
//   - Every TypeRef that referred to a promoted class as TypeValue is
//     lifted to TypeHandle. Existing TypeHandle refs are untouched.
//   - The class's own IsHandle flag is also set so downstream code
//     (proto message generation, dispatch table layout, free RPC
//     emission) picks it up as a handle class.
func postProcessHandleClasses(spec *apispec.APISpec) {
	// Collect qualified names of classes that should become handles.
	// Indexed by both QualName and short Name so we can resolve TypeRefs
	// whose Name has not yet been qualified.
	promote := make(map[string]bool)
	shortToQual := make(map[string]string)
	shortConflicts := make(map[string]bool)
	// needsHandle reports whether the class must be a handle because value
	// semantics are impossible:
	//   - Reference-typed fields: brace init fails with "reference member
	//     uninitialized".
	//   - Move-only fields (e.g., std::unique_ptr): the implicit copy ctor
	//     is deleted, so vector<T> / pass-by-value / `new T(t)` all fail.
	needsHandle := func(c *apispec.Class) bool {
		for _, f := range c.Fields {
			if f.Type.IsRef {
				return true
			}
			qt := f.Type.QualType
			if strings.Contains(qt, "unique_ptr") {
				return true
			}
		}
		return false
	}
	for i := range spec.Classes {
		c := &spec.Classes[i]
		// Non-copyable or non-default-constructible classes are promoted
		// regardless of method count.
		if needsHandle(c) {
			if c.QualName != "" {
				promote[c.QualName] = true
			}
			if c.Name != "" {
				if existing, ok := shortToQual[c.Name]; ok && existing != c.QualName {
					shortConflicts[c.Name] = true
				} else {
					shortToQual[c.Name] = c.QualName
				}
			}
			c.IsHandle = true
			continue
		}
		// Skip classes with no methods — nothing to bridge, nothing to
		// preserve. Forward declarations and tag types fall here.
		if len(c.Methods) == 0 {
			continue
		}
		// Pure POD: all state is visible via public fields, and the
		// bridge's value-flattening path captures it completely.
		if len(c.Fields) > 0 && !c.HasPrivateFields {
			continue
		}
		if c.QualName != "" {
			promote[c.QualName] = true
		}
		if c.Name != "" {
			if existing, ok := shortToQual[c.Name]; ok && existing != c.QualName {
				// Same short name in multiple namespaces — we can't
				// safely reclassify short-name TypeRefs without risking
				// cross-namespace confusion. Record the conflict so the
				// reclassify step below falls back to qualified-only.
				shortConflicts[c.Name] = true
			} else {
				shortToQual[c.Name] = c.QualName
			}
		}
		// Mark the class itself so downstream generators treat it as a
		// handle class. We don't touch HasPublicDefaultCtor etc. here;
		// those flags continue to reflect the physical C++ shape.
		c.IsHandle = true
	}
	if len(promote) == 0 {
		return
	}

	shouldPromote := func(name string) bool {
		if name == "" {
			return false
		}
		if promote[name] {
			return true
		}
		// Short name fallback: only if unambiguous.
		if !strings.Contains(name, "::") && !shortConflicts[name] {
			if q, ok := shortToQual[name]; ok && promote[q] {
				return true
			}
		}
		return false
	}

	var walk func(*apispec.TypeRef)
	walk = func(ref *apispec.TypeRef) {
		if ref == nil {
			return
		}
		if ref.Kind == apispec.TypeValue && shouldPromote(ref.Name) {
			ref.Kind = apispec.TypeHandle
		}
		if ref.Inner != nil {
			walk(ref.Inner)
		}
	}

	for i := range spec.Functions {
		fn := &spec.Functions[i]
		walk(&fn.ReturnType)
		for j := range fn.Params {
			walk(&fn.Params[j].Type)
		}
	}
	for i := range spec.Classes {
		c := &spec.Classes[i]
		for j := range c.Fields {
			walk(&c.Fields[j].Type)
		}
		for j := range c.Methods {
			m := &c.Methods[j]
			walk(&m.ReturnType)
			for k := range m.Params {
				walk(&m.Params[k].Type)
			}
		}
	}
}

// postProcessEnumTypes walks all TypeRefs in the spec and reclassifies
// handle/value types that actually refer to enums. classifyType runs before
// enums are fully collected, so it may misclassify enum types as handles
// (if pointer) or values. Additionally, when a short name maps to a uniquely
// qualified enum, replace the Name with the qualified name for later
// proto/bridge code generation.
//
// allowShortNamePromotion controls whether short-name heuristics run.
// Per-batch callers (umbrella header batches in parse-headers) must pass
// false: a short name like "Type" might uniquely resolve to enum A in
// batch 1 but to enum B in batch 2, and an eager per-batch rewrite
// locks in the wrong qualified name before the merged pass can see the
// full picture. The merged post-process pass passes true.
func postProcessEnumTypes(spec *apispec.APISpec, allowShortNamePromotion bool) {
	// Build a map of enum names → qualified name.
	// For short names that map to multiple enums (ambiguous), don't rewrite.
	shortToQual := make(map[string]string)
	shortCount := make(map[string]int)
	qualSet := make(map[string]bool)
	// Proto-mangled enum names like "ns::ResolvedJoinScanEnums_JoinType"
	// are exposed by the C++ class as a nested alias ("ns::ResolvedJoinScan
	// ::JoinType" via `using`). Clang records the method's return type
	// spelling as either the mangled form or the nested-alias form; we
	// must recognise both so postProcess correctly reclassifies them as
	// enums. For every mangled enum we also seed the alias name (with and
	// without project-namespace prefix) and a nested short form so the
	// value→enum demotion below fires on whichever spelling clang emitted.
	addEnumAliases := func(qualName string) {
		qualSet[qualName] = true
		// Also register the namespace-less form of any nested enum so
		// clang references using that spelling still match. Example:
		// qualName="ns::ASTAlterIndexStatement::IndexType" gets the
		// alias "ASTAlterIndexStatement::IndexType".
		if ns, rest, ok := strings.Cut(qualName, "::"); ok && ns != "" && strings.Contains(rest, "::") {
			qualSet[rest] = true
		}
		// Proto-mangled enums (ns::XEnums_Y) gain a nested-alias form
		// (ns::X::Y) that the C++ class exposes via `using Y =
		// XEnums_Y;`. Register both the namespaced and namespace-less
		// versions so postProcess reclassifies TypeRefs spelled either
		// way.
		marker := "Enums_"
		idx := strings.Index(qualName, marker)
		if idx < 0 {
			return
		}
		prefix := qualName[:idx]           // "ns::ResolvedJoinScan"
		suffix := qualName[idx+len(marker):] // "JoinType"
		if prefix == "" || suffix == "" {
			return
		}
		nestedFull := prefix + "::" + suffix // "ns::ResolvedJoinScan::JoinType"
		qualSet[nestedFull] = true
		if _, short, ok := strings.Cut(prefix, "::"); ok && short != "" {
			// With-namespace-stripped form: "ResolvedJoinScan::JoinType".
			nestedShort := short + "::" + suffix
			qualSet[nestedShort] = true
		}
	}
	for _, e := range spec.Enums {
		if e.Name == "" {
			continue
		}
		addEnumAliases(e.QualName)
		shortCount[e.Name]++
		if _, ok := shortToQual[e.Name]; !ok {
			shortToQual[e.Name] = e.QualName
		}
	}
	// Widen shortCount with "suffix ambiguity": if the short name is
	// "SqlSecurity" and there's also an enum whose qualified name ends in
	// "_SqlSecurity" (e.g. ASTCreateStatementEnums_SqlSecurity from a
	// protobuf-generated header), treat the short name as ambiguous.
	// Clang's AST often records a typedef'd enum's type spelling as the
	// bare short name, so short-name promotion would otherwise pick the
	// wrong fully-qualified enum.
	for _, e := range spec.Enums {
		q := e.QualName
		// Peel off everything up to the last segment separator, then
		// bump shortCount for every short name ambiguously matched.
		for short := range shortToQual {
			if short == e.Name {
				continue
			}
			if strings.HasSuffix(q, "::"+short) || strings.HasSuffix(q, "_"+short) {
				shortCount[short]++
			}
		}
	}
	if len(shortToQual) == 0 && len(qualSet) == 0 {
		return
	}

	// classNames tracks classes to avoid misclassifying a class as an enum
	// when the same short name is used by both.
	classNameSet := make(map[string]bool)
	classQualSet := make(map[string]bool)
	for _, c := range spec.Classes {
		if c.Name != "" {
			classNameSet[c.Name] = true
		}
		if c.QualName != "" {
			classQualSet[c.QualName] = true
		}
	}

	// isEnumExact: a name is definitively an enum only if it matches a
	// qualified enum name exactly (no ambiguity).
	isEnumExact := func(name string) bool {
		if name == "" {
			return false
		}
		return qualSet[name]
	}

	// nestedToQualified picks the namespaced form of a nested-enum name
	// out of qualSet (e.g., "ns::X::Y") when given its namespace-less
	// shorthand ("X::Y"). The bridge emits the name literally into C++
	// code inside `extern "C"`, where `using namespace X` is not in
	// effect, so the unqualified form would fail to compile.
	//
	// qualSet typically contains BOTH forms (the alias-adder seeds the
	// namespace-less version to help isEnumExact), so we must not stop
	// early on the self match: keep walking until we find a longer form
	// that ends in `::name`. If the name is already namespace-qualified
	// (starts with a namespace known to postProcess), bail out.
	nestedToQualified := func(name string) string {
		if name == "" || strings.HasPrefix(name, "::") {
			return ""
		}
		for q := range qualSet {
			if q == name {
				continue
			}
			if strings.HasSuffix(q, "::"+name) {
				return q
			}
		}
		return ""
	}

	reclassify := func(ref *apispec.TypeRef) {
		if ref == nil {
			return
		}
		if ref.Kind == apispec.TypeHandle || ref.Kind == apispec.TypeValue {
			// Exact qualified match wins first. We intentionally do NOT
			// rewrite the name to the proto-mangled form here — the C++
			// class defines its own nested enum that uses the nested
			// spelling ("ASTClass::EnumName"), while the proto schema
			// uses a separate flat enum ("ASTClassEnums_EnumName"). The
			// name in ref.Name is used to emit C++ type spellings in
			// the bridge, which must match the nested setter signature.
			// Downstream proto emission translates to the mangled form
			// via typeRefToProto's canonicalize helper.
			if isEnumExact(ref.Name) {
				ref.Kind = apispec.TypeEnum
				// If clang gave us the namespace-less form of a nested
				// enum, fill in the namespace so C++ emission compiles
				// outside the project namespace block.
				if qualified := nestedToQualified(ref.Name); qualified != "" {
					ref.Name = qualified
				}
			} else if allowShortNamePromotion &&
				!strings.Contains(ref.Name, "::") &&
				shortCount[ref.Name] == 1 &&
				!classNameSet[ref.Name] {
				// Short name that uniquely resolves to one enum and
				// does not clash with any class — safe to reclassify.
				// We also promote the name to its qualified form.
				ref.Kind = apispec.TypeEnum
				ref.Name = shortToQual[ref.Name]
			}
		}
		// If already classified as enum, make sure the name is at least
		// namespace-qualified. This path fires on refs that postProcess
		// has seen (and reclassified) before, e.g. parameter types on
		// overloads that lost the namespace during an earlier batch.
		if ref.Kind == apispec.TypeEnum && strings.Contains(ref.Name, "::") {
			if qualified := nestedToQualified(ref.Name); qualified != "" {
				ref.Name = qualified
			}
		}
		// If already classified as enum but name is unqualified AND
		// unambiguous AND not shared with a class, qualify it.
		if allowShortNamePromotion &&
			ref.Kind == apispec.TypeEnum && !strings.Contains(ref.Name, "::") {
			if shortCount[ref.Name] == 1 && !classNameSet[ref.Name] {
				ref.Name = shortToQual[ref.Name]
			}
		}
	}

	var walk func(*apispec.TypeRef)
	walk = func(ref *apispec.TypeRef) {
		if ref == nil {
			return
		}
		reclassify(ref)
		if ref.Inner != nil {
			walk(ref.Inner)
		}
	}

	for i := range spec.Functions {
		fn := &spec.Functions[i]
		walk(&fn.ReturnType)
		for j := range fn.Params {
			walk(&fn.Params[j].Type)
		}
	}
	for i := range spec.Classes {
		c := &spec.Classes[i]
		for j := range c.Fields {
			walk(&c.Fields[j].Type)
		}
		for j := range c.Methods {
			m := &c.Methods[j]
			walk(&m.ReturnType)
			for k := range m.Params {
				walk(&m.Params[k].Type)
			}
		}
	}
}

// ParseStream extracts API information by streaming the clang AST JSON from a reader.
// This recursively streams through nested structures (namespaces, linkage specs)
// without loading them fully into memory. Only declarations matching the target
// header file are fully decoded.
func ParseStream(r io.Reader, headerFile string) (*apispec.APISpec, error) {
	return ParseStreamMulti(r, []string{headerFile})
}

// ParseStreamMulti is like ParseStream but accepts multiple target header files.
// This is used with an umbrella header that includes all public headers:
// clang processes one compilation, and we match declarations from any target header.
func ParseStreamMulti(r io.Reader, headerFiles []string) (*apispec.APISpec, error) {
	return ParseStreamMultiWithOptions(r, headerFiles, nil)
}

// ParseStreamMultiWithOptions is the option-bearing form of
// ParseStreamMulti. allowedExternalClasses lists fully-qualified
// class names (e.g. "google::protobuf::DescriptorPool") that the
// parser should retain even when their declaration site is
// outside the project header set. Pass nil to fall back to the
// project-only filter.
func ParseStreamMultiWithOptions(r io.Reader, headerFiles []string, allowedExternalClasses []string) (*apispec.APISpec, error) {
	p := newParser(headerFiles)
	if len(allowedExternalClasses) > 0 {
		p.SetAllowedExternalClasses(allowedExternalClasses)
	}

	dec := json.NewDecoder(r)

	// Navigate into the root TranslationUnitDecl's "inner" array
	if err := navigateToInner(dec); err != nil {
		return nil, err
	}

	// Process top-level declarations
	// Start with empty file context — each node will set its own file
	if err := p.streamInnerArray(dec, "", ""); err != nil {
		return nil, err
	}

	// Flatten classes map
	// Re-resolve qual names for classes whose parent class was visited
	// AFTER the class itself — see fixupNestedClassQualNames for why.
	p.fixupNestedClassQualNames()
	// Expand namespace-scope typedefs now that the full alias map is
	// known — see postProcessTypedefAliases for the motivation.
	p.postProcessTypedefAliases()
	for _, c := range p.classes {
		p.spec.Classes = append(p.spec.Classes, *c)
	}
	// Re-write unqualified type references with their fully-qualified
	// form by walking the namespace / class scope. clang's
	// `qualType` field preserves the as-written spelling so a
	// parameter declared inside `namespace google::protobuf` as
	// `SourceLocation*` arrives here as the bare string
	// `SourceLocation *`. Without this pass, downstream code has to
	// disambiguate short names against an ambiguous global pool and
	// can pick the wrong namespace's class.
	p.postProcessQualifyShortNames()
	// Per-batch pass: disable short-name promotion. A short name like
	// "Type" may look unambiguous inside a single batch but collide with
	// another enum (or a class) once all batches are merged. The merged
	// pass in cmdParseHeaders re-runs postProcessEnumTypes with short-
	// name promotion enabled, so enum refs are still caught there.
	postProcessEnumTypes(p.spec, false)
	postProcessHandleClasses(p.spec)
	return p.spec, nil
}

// streamInnerArray processes each element in a JSON array using streaming.
// It reads elements one at a time, peeking at kind/location to decide
// whether to fully decode or skip via brace-depth counting.
//
// currentFile tracks the file context across siblings. Clang uses location
// compression: when consecutive nodes are from the same file, later nodes
// omit the "file" field. We track the actual file path (not just a boolean)
// so we can correctly attribute nodes to their source file even after
// processing nodes from non-target files.
func (p *Parser) streamInnerArray(dec *json.Decoder, namespace string, currentFile string) error {
	for dec.More() {
		node, skipped, updatedFile, err := p.streamObject(dec, namespace, currentFile)
		if err != nil {
			return err
		}
		// Update file context from this sibling for subsequent siblings
		currentFile = updatedFile
		if skipped {
			continue
		}
		if node != nil {
			p.walk(node, namespace, p.matchesTarget(currentFile), currentFile)
		}
	}

	// Read closing ']'
	if _, err := dec.Token(); err != nil {
		return fmt.Errorf("reading array end: %w", err)
	}
	return nil
}

// streamObject reads a single JSON object from the decoder.
// It peeks at "kind", "name", "loc", "range" fields to decide handling:
//   - NamespaceDecl/LinkageSpecDecl: recursively stream their "inner" array
//   - FunctionDecl/CXXRecordDecl/EnumDecl from target file: fully decode and return
//   - Everything else: skip by depth counting
//
// Returns (node, skipped, updatedFile, error).
// updatedFile reflects the file path after processing this node, for sibling propagation
// (clang compresses locations: later siblings inherit file from earlier ones).
func (p *Parser) streamObject(dec *json.Decoder, namespace string, currentFile string) (*Node, bool, string, error) {
	// Read opening '{'
	tok, err := dec.Token()
	if err != nil {
		return nil, false, currentFile, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, false, currentFile, fmt.Errorf("expected '{', got %v", tok)
	}

	// Read fields, collecting metadata we need for filtering
	var kind, name string
	var loc *Loc
	var rng *Range

	// First pass: read fields looking for kind, name, loc, range.
	// Buffer other fields in case we need to reconstruct the full node.
	pendingFields := make(map[string]json.RawMessage)

	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, false, currentFile, err
		}
		key := keyTok.(string)

		switch key {
		case "kind":
			var v string
			if err := dec.Decode(&v); err != nil {
				return nil, false, currentFile, err
			}
			kind = v
		case "name":
			var v string
			if err := dec.Decode(&v); err != nil {
				return nil, false, currentFile, err
			}
			name = v
		case "loc":
			var v Loc
			if err := dec.Decode(&v); err != nil {
				return nil, false, currentFile, err
			}
			loc = &v
		case "range":
			var v Range
			if err := dec.Decode(&v); err != nil {
				return nil, false, currentFile, err
			}
			rng = &v
		case "inner":
			// Determine this node's file: use explicit file if present,
			// otherwise inherit from currentFile (clang location compression).
			nodeFile := locFile(loc, rng)
			if nodeFile == "" {
				nodeFile = currentFile
			}

			switch kind {
			case "NamespaceDecl":
				// Always recurse into namespaces — they may contain declarations
				// from multiple files, and filtering happens at leaf level.
				ns := namespace
				if name != "" {
					if ns != "" {
						ns += "::"
					}
					ns += name
				}
				if err := expectToken(dec, json.Delim('[')); err != nil {
					return nil, false, nodeFile, err
				}
				if err := p.streamInnerArray(dec, ns, nodeFile); err != nil {
					return nil, false, nodeFile, err
				}
				skipRemainingFields(dec)
				return nil, true, nodeFile, nil

			case "LinkageSpecDecl":
				// Always recurse into linkage specs
				if err := expectToken(dec, json.Delim('[')); err != nil {
					return nil, false, nodeFile, err
				}
				if err := p.streamInnerArray(dec, namespace, nodeFile); err != nil {
					return nil, false, nodeFile, err
				}
				skipRemainingFields(dec)
				return nil, true, nodeFile, nil

			case "FunctionDecl", "CXXRecordDecl", "EnumDecl", "TypedefDecl", "TypeAliasDecl":
				// These are interesting if from target file — or if the
				// user has listed the class qualified name in
				// bridge.ExternalTypes (mirrored into
				// allowedExternalClasses). Buffer inner so the decode
				// site can re-check after the full qual name resolves.
				nodeInTarget := p.matchesTarget(nodeFile) || p.qualNameAllowed(namespace, name)
				if !nodeInTarget {
					skipValue(dec)
					skipRemainingFields(dec)
					return nil, true, nodeFile, nil
				}
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					return nil, false, nodeFile, err
				}
				pendingFields["inner"] = raw

			default:
				skipValue(dec)
				skipRemainingFields(dec)
				return nil, true, nodeFile, nil
			}

		default:
			// Buffer the field into pendingFields. Unlike the old implementation
			// which only buffered when kind was already known, this always buffers
			// because some important fields (id, parentDeclContextId) may appear
			// BEFORE kind in the JSON output. We can't skip them based on kind
			// since kind hasn't been read yet.
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return nil, false, currentFile, err
			}
			pendingFields[key] = raw
		}
	}

	// Read closing '}'
	if _, err := dec.Token(); err != nil {
		return nil, false, currentFile, err
	}

	// Determine this node's file
	nodeFile := locFile(loc, rng)
	if nodeFile == "" {
		nodeFile = currentFile
	}

	switch kind {
	case "FunctionDecl", "CXXRecordDecl", "EnumDecl", "TypedefDecl", "TypeAliasDecl":
		if !p.matchesTarget(nodeFile) && !p.qualNameAllowed(namespace, name) {
			return nil, true, nodeFile, nil
		}
		node, err := reconstructNode(kind, name, loc, rng, pendingFields)
		if err != nil {
			return nil, true, nodeFile, nil
		}
		return node, false, nodeFile, nil

	case "NamespaceDecl", "LinkageSpecDecl":
		return nil, true, nodeFile, nil

	default:
		return nil, true, nodeFile, nil
	}
}

// reconstructNode builds a Node from the collected fields.
func reconstructNode(kind, name string, loc *Loc, rng *Range, fields map[string]json.RawMessage) (*Node, error) {
	// Build a JSON object from the collected fields + metadata
	obj := make(map[string]json.RawMessage)
	for k, v := range fields {
		obj[k] = v
	}

	// Add metadata fields
	kindJSON, _ := json.Marshal(kind)
	obj["kind"] = kindJSON
	if name != "" {
		nameJSON, _ := json.Marshal(name)
		obj["name"] = nameJSON
	}
	if loc != nil {
		locJSON, _ := json.Marshal(loc)
		obj["loc"] = locJSON
	}
	if rng != nil {
		rngJSON, _ := json.Marshal(rng)
		obj["range"] = rngJSON
	}

	data, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}

	var node Node
	if err := json.Unmarshal(data, &node); err != nil {
		return nil, err
	}
	return &node, nil
}

// skipValue reads and discards a single JSON value from the decoder.
// It handles nested objects and arrays by counting depth.
func skipValue(dec *json.Decoder) {
	tok, err := dec.Token()
	if err != nil {
		return
	}
	if d, ok := tok.(json.Delim); ok {
		switch d {
		case '{':
			depth := 1
			for depth > 0 {
				t, err := dec.Token()
				if err != nil {
					return
				}
				if dd, ok := t.(json.Delim); ok {
					switch dd {
					case '{', '[':
						depth++
					case '}', ']':
						depth--
					}
				}
			}
		case '[':
			depth := 1
			for depth > 0 {
				t, err := dec.Token()
				if err != nil {
					return
				}
				if dd, ok := t.(json.Delim); ok {
					switch dd {
					case '{', '[':
						depth++
					case '}', ']':
						depth--
					}
				}
			}
		}
	}
	// Scalar values (string, number, bool, null) are consumed by Token() already
}

// skipRemainingFields reads and discards remaining fields in the current object,
// then reads the closing '}'.
func skipRemainingFields(dec *json.Decoder) {
	for dec.More() {
		// Read field name
		if _, err := dec.Token(); err != nil {
			return
		}
		// Skip field value
		skipValue(dec)
	}
	// Read closing '}'
	_, _ = dec.Token()
}

// navigateToInner reads the root object and navigates to its "inner" array.
func navigateToInner(dec *json.Decoder) error {
	// Read opening '{'
	if err := expectToken(dec, json.Delim('{')); err != nil {
		return fmt.Errorf("expected root object: %w", err)
	}

	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("reading root field: %w", err)
		}
		key, ok := tok.(string)
		if !ok {
			continue
		}
		if key == "inner" {
			// Read opening '['
			if err := expectToken(dec, json.Delim('[')); err != nil {
				return fmt.Errorf("expected inner array: %w", err)
			}
			return nil
		}
		// Skip the value of this field
		skipValue(dec)
	}
	return fmt.Errorf("no 'inner' array found in TranslationUnitDecl")
}

// expectToken reads the next token and verifies it matches expected.
func expectToken(dec *json.Decoder, expected json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := tok.(json.Delim)
	if !ok || d != expected {
		return fmt.Errorf("expected %v, got %v", expected, tok)
	}
	return nil
}

// locFile extracts the file path from Loc or Range, checking all possible locations.
// Clang uses different location representations:
// - Direct file: loc.file
// - Range: range.begin.file
// - Spelling location: loc.spellingLoc.file (for macro-expanded or included code)
// - Expansion location: loc.expansionLoc.file
func locFile(loc *Loc, rng *Range) string {
	if loc != nil {
		if loc.File != "" {
			return loc.File
		}
		// Prefer expansion location over spelling location for macro-expanded
		// declarations. ExpansionLoc points to where the macro was used (the
		// user's header), while SpellingLoc points to where the macro is
		// defined (e.g., absl/base/attributes.h for ABSL_DEPRECATED).
		// Using SpellingLoc first would incorrectly attribute declarations
		// annotated with macros like ABSL_DEPRECATED to the macro's header,
		// causing all subsequent siblings (which inherit file context via
		// clang's location compression) to be misattributed as well.
		if loc.ExpansionLoc != nil && loc.ExpansionLoc.File != "" {
			return loc.ExpansionLoc.File
		}
		if loc.SpellingLoc != nil && loc.SpellingLoc.File != "" {
			return loc.SpellingLoc.File
		}
	}
	if rng != nil {
		if rng.Begin.File != "" {
			return rng.Begin.File
		}
		if rng.Begin.ExpansionLoc != nil && rng.Begin.ExpansionLoc.File != "" {
			return rng.Begin.ExpansionLoc.File
		}
		if rng.Begin.SpellingLoc != nil && rng.Begin.SpellingLoc.File != "" {
			return rng.Begin.SpellingLoc.File
		}
	}
	return ""
}

// walk traverses the AST tree. inTargetFile tracks whether the parent node's
// file context indicates we're inside the target header. Clang compresses
// location info — child nodes often have empty file, inheriting from their parent.
func (p *Parser) walk(node *Node, namespace string, inTargetFile bool, parentFile string) {
	if node == nil {
		return
	}

	// Update file context: if this node has explicit file info, use it.
	// Otherwise inherit from parent (clang compresses location info).
	nodeFile := p.nodeFile(node)
	if nodeFile == "" {
		nodeFile = parentFile
	}
	nodeInTarget := inTargetFile
	if nodeFile != "" {
		nodeInTarget = p.matchesTarget(nodeFile) || p.qualNameAllowed(namespace, node.Name)
	}

	switch node.Kind {
	case "NamespaceDecl":
		ns := namespace
		if node.Name != "" {
			if ns != "" {
				ns += "::"
			}
			ns += node.Name
		}
		for i := range node.Inner {
			p.walk(&node.Inner[i], ns, nodeInTarget, nodeFile)
		}
		return

	case "FunctionDecl":
		if node.IsImplicit || node.StorageClass == "static" {
			break
		}
		if !nodeInTarget {
			break
		}
		// Skip template specializations: they have inner TemplateArgument.
		// The template's general form cannot be called without specifying
		// template arguments, and specializations require the exact type
		// to be used at the call site.
		if isTemplateSpecialization(node) {
			break
		}
		fn := p.parseFunction(node, namespace)
		if fn != nil {
			fn.SourceFile = normalizeSourceFile(nodeFile)
			fn.Comment = extractComment(node)
			p.spec.Functions = append(p.spec.Functions, *fn)
		}

	case "CXXRecordDecl":
		if node.IsImplicit || node.Name == "" {
			break
		}
		if !nodeInTarget {
			break
		}
		p.parseClass(node, namespace)
		qualName := node.Name
		if namespace != "" {
			qualName = namespace + "::" + node.Name
		}
		if cls, ok := p.classes[qualName]; ok {
			if cls.SourceFile == "" {
				cls.SourceFile = normalizeSourceFile(nodeFile)
			}
			if cls.Comment == "" {
				cls.Comment = extractComment(node)
			}
		}

	case "EnumDecl":
		if node.IsImplicit || node.Name == "" {
			break
		}
		if !nodeInTarget {
			break
		}
		e := p.parseEnum(node, namespace)
		if e != nil {
			e.SourceFile = normalizeSourceFile(nodeFile)
			e.Comment = extractComment(node)
			p.spec.Enums = append(p.spec.Enums, *e)
		}

	case "TypedefDecl", "TypeAliasDecl":
		// File- or namespace-scope typedef. Record alias -> underlying
		// so param/return types that mention the alias (e.g.
		// `FunctionArgumentTypeList` instead of
		// `std::vector<FunctionArgumentType>`) can be expanded at
		// classify time. Also remember the typedef's namespace so we
		// can fully qualify any unqualified class identifiers that
		// appear inside the underlying spelling.
		if node.Name != "" && node.Type != nil && node.Type.QualType != "" {
			info := aliasInfo{
				Underlying: node.Type.QualType,
				Namespace:  namespace,
			}
			qual := node.Name
			if namespace != "" {
				qual = namespace + "::" + node.Name
			}
			p.globalAliases[qual] = info
			// Also index the bare name so lookups against params that
			// spell the alias without any namespace prefix still hit.
			if _, exists := p.globalAliases[node.Name]; !exists {
				p.globalAliases[node.Name] = info
			}
		}

	case "TranslationUnitDecl":
		for i := range node.Inner {
			p.walk(&node.Inner[i], namespace, nodeInTarget, nodeFile)
		}
		return

	case "LinkageSpecDecl":
		for i := range node.Inner {
			p.walk(&node.Inner[i], namespace, nodeInTarget, nodeFile)
		}
		return
	}
}

// nodeFile returns the file path from a node's location info, or "" if absent.
func (p *Parser) nodeFile(node *Node) string {
	return locFile(node.Loc, node.Range)
}

// matchesHeaderFile checks if file matches the target header.
// Handles exact path match, suffix match, and path-component suffix match.
// The component match is needed because clang may resolve headers through
// different include paths (e.g., Bazel's execroot vs project directory),
// resulting in different absolute prefixes but identical relative paths.
func matchesHeaderFile(file, headerFile string) bool {
	if file == headerFile {
		return true
	}
	// Suffix match: one path ends with the other
	if strings.HasSuffix(file, "/"+headerFile) || strings.HasSuffix(file, headerFile) {
		return true
	}
	if strings.HasSuffix(headerFile, "/"+file) || strings.HasSuffix(headerFile, file) {
		return true
	}
	// Path-component suffix match: find the longest common suffix of path components.
	// e.g., "/bazel/execroot/_main/mylib/public/foo.h" and
	//        "/project/testdata/mylib/mylib/public/foo.h"
	// both have suffix "mylib/public/foo.h" (3+ components).
	// Require at least 2 components to match (dir + filename) to avoid false positives.
	fileParts := strings.Split(file, "/")
	headerParts := strings.Split(headerFile, "/")
	fi := len(fileParts) - 1
	hi := len(headerParts) - 1
	matched := 0
	for fi >= 0 && hi >= 0 && fileParts[fi] == headerParts[hi] {
		matched++
		fi--
		hi--
	}
	// At least dir/filename (2 components) must match
	return matched >= 2
}

func (p *Parser) parseFunction(node *Node, namespace string) *apispec.Function {
	fn := &apispec.Function{
		Name:      node.Name,
		Namespace: namespace,
	}
	if namespace != "" {
		fn.QualName = namespace + "::" + node.Name
	} else {
		fn.QualName = node.Name
	}

	// Parse return type and parameters from inner nodes
	if node.Type != nil {
		fn.ReturnType = p.parseReturnTypeFromNode(node.Type)
	}

	for i := range node.Inner {
		child := &node.Inner[i]
		if child.Kind == "ParmVarDecl" {
			param := apispec.Param{
				Name: child.Name,
			}
			if child.Type != nil {
				param.Type = p.classifyTypeFromNode(child.Type)
			}
			fn.Params = append(fn.Params, param)
		}
	}

	return fn
}

func (p *Parser) parseClass(node *Node, namespace string) {
	// Resolve out-of-line nested class definitions. clang represents
	// `class Outer::Nested { ... }` (defined outside Outer's body) as a
	// top-level CXXRecordDecl with a parentDeclContextId pointing to Outer.
	// In this case, the namespace should be Outer's qualified name.
	deferredParentID := ""
	if node.ParentDeclContextID != "" {
		if parentQual, ok := p.classIDs[node.ParentDeclContextID]; ok {
			namespace = parentQual
		} else {
			// Parent not yet registered. Parse the class now under the
			// best-available namespace but record the parent ID so the
			// deferred fixup can repair qualName once the parent is
			// finally visited.
			deferredParentID = node.ParentDeclContextID
		}
	}

	qualName := node.Name
	if namespace != "" {
		qualName = namespace + "::" + node.Name
	}
	if deferredParentID != "" {
		p.classParentIDs[qualName] = deferredParentID
	}

	// Skip forward declarations - we only want complete class definitions.
	// A forward declaration has neither DefinitionData nor inner members.
	// This filter prevents nested classes from being recorded twice with
	// incorrect namespace attribution (once via outer-scope forward decl,
	// once via the actual nested definition).
	if node.DefinitionData == nil && len(node.Inner) == 0 {
		// Still record the ID → qualName mapping so children that reference
		// this declaration via parentDeclContextId can resolve it.
		if node.ID != "" {
			p.classIDs[node.ID] = qualName
		}
		return
	}

	cls, exists := p.classes[qualName]
	if !exists {
		cls = &apispec.Class{
			Name:      node.Name,
			Namespace: namespace,
			QualName:  qualName,
		}
		p.classes[qualName] = cls
	}

	// Register this declaration's ID → qualName for parent-context resolution
	// of out-of-line nested class definitions that reference us.
	if node.ID != "" {
		p.classIDs[node.ID] = qualName
	}

	// Check if abstract/polymorphic
	if node.DefinitionData != nil {
		cls.IsAbstract = node.DefinitionData.IsAbstract
		if node.DefinitionData.IsPolymorphic {
			cls.IsHandle = true
		}
		// Determine if class has an accessible default constructor.
		// A class can be default-constructed if:
		// - defaultCtor.exists is true, OR
		// - it has no user-declared constructor (implicit default ctor)
		// AND the default ctor is not explicitly deleted (we check member ctors below).
		hasDefault := false
		if dd := node.DefinitionData; dd.DefaultCtor != nil {
			if dd.DefaultCtor.Exists || dd.DefaultCtor.NeedsImplicit {
				hasDefault = true
			}
		}
		cls.HasPublicDefaultCtor = hasDefault
		// Default: destructor is public (implicit). Member loop below may
		// override this if an explicit non-public destructor is declared.
		cls.HasPublicDtor = true
	}

	// Parse base classes
	for _, base := range node.Bases {
		parentName := cleanTypeName(base.Type.QualType)
		parentName = p.resolveQualName(parentName, namespace)
		cls.Parents = append(cls.Parents, parentName)
		if cls.Parent == "" {
			cls.Parent = parentName
		}
		cls.IsHandle = true // classes with inheritance are typically handle types
	}

	// Determine default access: `class` defaults to private, `struct` to public.
	// If TagUsed is empty (e.g., in synthetic test data), default to public to
	// preserve the legacy behavior where the caller set member access directly.
	currentAccess := "public"
	if node.TagUsed == "class" {
		currentAccess = "private"
	}

	// memberAccess returns the effective access for a child node. clang AST
	// does NOT put `access` on individual members at emission time; instead,
	// AccessSpecDecl nodes update a running access spec that applies to all
	// subsequent members. However, our synthetic test data and legacy code
	// sometimes sets `child.Access` directly - honor that if present.
	memberAccess := func(child *Node) string {
		if child.Access != "" {
			return child.Access
		}
		return currentAccess
	}

	for i := range node.Inner {
		child := &node.Inner[i]

		switch child.Kind {
		case "AccessSpecDecl":
			// Update current access for subsequent members
			if child.Access != "" {
				currentAccess = child.Access
			}
			continue

		case "TypeAliasDecl", "TypedefDecl":
			// Record class-scoped type aliases (`using Value = intptr_t;`
			// or `typedef intptr_t Value;`). These are indistinguishable
			// from a real class type at the method return-type level
			// (`Value GetNext()`), so we remember the underlying type
			// here and substitute at classify time.
			if child.Name != "" && child.Type != nil && child.Type.QualType != "" {
				m := p.classAliases[qualName]
				if m == nil {
					m = make(map[string]string)
					p.classAliases[qualName] = m
				}
				m[child.Name] = child.Type.QualType
			}
			continue

		case "CXXMethodDecl", "CXXConstructorDecl", "CXXDestructorDecl":
			if child.IsImplicit {
				continue
			}
			// Detect deleted operator new (e.g., arena-allocated protobuf messages)
			if child.Kind == "CXXMethodDecl" && child.Name == "operator new" && child.ExplicitlyDeleted {
				cls.HasDeletedOperatorNew = true
			}
			// Track constructor accessibility for HasPublicDefaultCtor and
			// HasDeletedCopyCtor, and add public non-deleted constructors
			// to the method list so the bridge can generate `new ClassName(...)`
			// dispatch cases.
			if child.Kind == "CXXConstructorDecl" {
				access := memberAccess(child)
				kind := constructorKind(child)
				switch kind {
				case "default":
					if child.ExplicitlyDeleted || access != "public" {
						cls.HasPublicDefaultCtor = false
					} else {
						cls.HasPublicDefaultCtor = true
					}
				case "copy":
					if child.ExplicitlyDeleted {
						cls.HasDeletedCopyCtor = true
					}
				}
				// Skip non-public, explicitly-deleted, or copy/move ctors.
				// Copy/move ctors are not useful as top-level constructors
				// because the source object would need to already exist as
				// a handle anyway.
				if access != "public" || child.ExplicitlyDeleted {
					continue
				}
				if kind == "copy" || kind == "move" {
					continue
				}
				ctor := p.parseFunctionAsMethod(child, qualName)
				if ctor != nil {
					ctor.Access = "public"
					ctor.IsConstructor = true
					ctor.Comment = extractComment(child)
					// Set return type to indicate it produces the class (as a handle)
					ctor.ReturnType = apispec.TypeRef{
						Name:      qualName,
						Kind:      apispec.TypeHandle,
						IsPointer: true,
					}
					cls.Methods = append(cls.Methods, *ctor)
				}
				continue
			}
			if child.Kind == "CXXDestructorDecl" {
				// Track destructor accessibility for HasPublicDtor.
				access := memberAccess(child)
				if access != "public" || child.ExplicitlyDeleted {
					cls.HasPublicDtor = false
				}
				continue
			}
			if memberAccess(child) != "public" {
				continue
			}
			method := p.parseFunctionAsMethod(child, qualName)
			if method != nil {
				method.Access = "public"
				method.Comment = extractComment(child)
				cls.Methods = append(cls.Methods, *method)
			}

		case "FieldDecl":
			// Non-public fields aren't exposed in the bridge, but we
			// still need to record their existence so handle-class
			// promotion can distinguish a true POD (public fields only)
			// from an encapsulated class with hidden state. Without
			// this, a class like
			//     class Foo { public: int id; private: std::vector<int> state_; };
			// would be flattened to `{ id }` and Foo::state_ would be
			// lost across bridge calls.
			if memberAccess(child) != "public" {
				cls.HasPrivateFields = true
				continue
			}
			field := apispec.Field{
				Name: child.Name,
			}
			if child.Type != nil {
				field.Type = p.classifyTypeFromNode(child.Type)
			}
			field.Access = "public"
			field.Comment = extractComment(child)
			cls.Fields = append(cls.Fields, field)

		case "CXXRecordDecl":
			// Nested class - always parse to register the AST id in
			// classIDs (so out-of-line definitions like
			//   class Outer::Nested::Inner { ... }
			// placed in a separate .inl file can resolve their parent
			// chain when they appear as top-level CXXRecordDecls in the
			// AST). If the nested class is not publicly accessible we
			// remove it from the spec afterwards, keeping only the ID
			// registration.
			if child.Name != "" && !child.IsImplicit {
				access := memberAccess(child)
				nestedQual := qualName + "::" + child.Name
				_, alreadyExisted := p.classes[nestedQual]
				p.parseClass(child, qualName)
				if access != "public" && !alreadyExisted {
					delete(p.classes, nestedQual)
				}
			}

		case "EnumDecl":
			if child.Name != "" && !child.IsImplicit && memberAccess(child) == "public" {
				e := p.parseEnum(child, qualName)
				if e != nil {
					p.spec.Enums = append(p.spec.Enums, *e)
				}
			}
		}
	}

	// Determine if handle: classes with virtual methods or used via pointer typically
	if !cls.IsHandle {
		for _, m := range cls.Methods {
			if m.IsVirtual {
				cls.IsHandle = true
				break
			}
		}
	}

	// Substitute class-scoped type aliases in method return/param types.
	// The clang AST reports the sugared name (e.g. "Value"), which we
	// already misclassified as a handle/value kind. If the class defines
	// `using Value = intptr_t;` we replace that TypeRef with one derived
	// from the underlying type.
	if aliases := p.classAliases[qualName]; aliases != nil {
		// isAliasSubstitutable reports whether the alias target's
		// shape is one we can faithfully marshal across the wire.
		// Two patterns qualify:
		//
		//   1. Class-rename alias —
		//        `using Column = TVFSchemaColumn;`
		//      Bare class identifier (qualified or not).
		//
		//   2. Container-of-class alias —
		//        `using ColumnList = vector<Column>;`
		//      A single-level template (`vector<X>`, `set<X>`,
		//      etc.) whose argument is a bare class identifier.
		//      Substitution recurses into Inner so the element
		//      ends up resolved to its underlying class.
		//
		// Anything deeper (e.g. `using PathParts =
		// vector<vector<string_view>>` — a container of a
		// container) is rejected. Those shapes need view-lifetime
		// handling the bridge dispatch can't yet provide.
		isBareIdent := func(s string) bool {
			t := strings.TrimSpace(s)
			t = strings.TrimPrefix(t, "const ")
			t = strings.TrimSpace(strings.TrimRight(t, "*& "))
			return t != "" && !strings.ContainsAny(t, "<,&* ")
		}
		isAliasSubstitutable := func(s string) bool {
			t := strings.TrimSpace(s)
			if isBareIdent(t) {
				return true
			}
			// Single-level template: <one identifier inside>.
			if i := strings.Index(t, "<"); i > 0 && strings.HasSuffix(t, ">") {
				inner := t[i+1 : len(t)-1]
				inner = strings.TrimSpace(inner)
				return isBareIdent(inner)
			}
			return false
		}
		var substitute func(ref *apispec.TypeRef, insideTemplate bool)
		substitute = func(ref *apispec.TypeRef, insideTemplate bool) {
			if ref == nil {
				return
			}
			// Recurse into Inner so vector / set / map element
			// types in this class's signatures are alias-resolved
			// too. (Without the recursion, e.g. `vector<Column>`
			// in a TVFRelation method keeps Inner.Name="Column",
			// which the bridge generator can't resolve to the
			// underlying TVFSchemaColumn class.)
			if ref.Inner != nil {
				substitute(ref.Inner, insideTemplate || ref.Kind == apispec.TypeVector)
			}
			if ref.Name == "" {
				return
			}
			underlying, ok := aliases[ref.Name]
			if !ok {
				return
			}
			// Skip aliases the bridge can't yet round-trip:
			//   * vector-of-vector etc. at top level.
			//   * any template alias when we're already inside a
			//     template (would expand to a nested template the
			//     wire-format layer doesn't yet support).
			if !isAliasSubstitutable(underlying) {
				return
			}
			if insideTemplate && !isBareIdent(underlying) {
				return
			}
			// Reclassify based on the underlying type. Preserve const/
			// pointer/ref qualifiers from the original reference since
			// those apply on top of the alias (e.g. `const Value&`).
			resolved := p.classifyType(underlying)
			// Only substitute when the original TypeRef would be a
			// handle/value (wrong classification); primitive/enum/vector
			// etc. mean the name happened to already classify correctly.
			if ref.Kind != apispec.TypeHandle && ref.Kind != apispec.TypeValue {
				return
			}
			// If the underlying type is complex (not a simple primitive,
			// string, or enum), prefer the underlying class's actual
			// qualified name so the bridge generator can look it up
			// in classQualNames / classSourceFiles. The previous
			// behaviour ("qualify the alias with enclosing class")
			// produced names like `TVFRelation::Column` that no class
			// is registered under — bridges of methods touching them
			// were silently filtered out. Falling back to the alias
			// path (qualify-with-enclosing-class) is preserved for
			// the case where the underlying spelling is itself a
			// nested name we couldn't resolve to a known class.
			if resolved.Kind == apispec.TypeValue || resolved.Kind == apispec.TypeHandle {
				// Class-rename alias (e.g. `using Column = TVFSchemaColumn;`).
				// Strip qualifiers off the underlying spelling so we can
				// look it up against the known class set. clang emits the
				// underlying type with a leading `::` global-scope marker
				// for qualified using-decls like
				// `using StructField = ::googlesql::StructField;` --
				// strip it before the class-set lookup, otherwise the
				// `p.classes` map (keyed without the leading `::`) misses
				// and the alias falls through to the
				// `EnclosingClass::AliasName` fallback path, producing a
				// bogus FQDN that no class is registered under.
				underlyingName := strings.TrimSpace(underlying)
				underlyingName = strings.TrimPrefix(underlyingName, "const ")
				underlyingName = strings.TrimSpace(strings.TrimRight(underlyingName, "*& "))
				underlyingName = strings.TrimPrefix(underlyingName, "::")
				resolvedQual := ""
				if underlyingName != "" {
					if _, known := p.classes[underlyingName]; known {
						// Already fully qualified.
						resolvedQual = underlyingName
					} else {
						// Short-name lookup against the known class set.
						match := ""
						ambiguous := false
						for q := range p.classes {
							short := q
							if i := strings.LastIndex(q, "::"); i >= 0 {
								short = q[i+2:]
							}
							if short == underlyingName {
								if match != "" && match != q {
									ambiguous = true
									break
								}
								match = q
							}
						}
						if !ambiguous && match != "" {
							resolvedQual = match
						}
					}
				}
				if resolvedQual != "" {
					resolved.QualType = resolvedQual
					resolved.Name = resolvedQual
				} else {
					// Couldn't resolve. Fall back to
					// `EnclosingClass::AliasName` so at least the
					// spelling is unambiguous within the project.
					qualAlias := qualName + "::" + ref.Name
					resolved.QualType = qualAlias
					resolved.Name = qualAlias
				}
			}
			resolved.IsConst = ref.IsConst || resolved.IsConst
			resolved.IsPointer = ref.IsPointer || resolved.IsPointer
			resolved.IsRef = ref.IsRef || resolved.IsRef
			*ref = resolved
			// After substituting the outer ref (e.g. ColumnList →
			// vector<Column>), recurse into the new Inner so the
			// element type is also alias-resolved
			// (Column → TVFRelation::Column → TVFSchemaColumn).
			if ref.Inner != nil {
				substitute(ref.Inner, insideTemplate || ref.Kind == apispec.TypeVector)
			}
		}
		for i := range cls.Methods {
			substitute(&cls.Methods[i].ReturnType, false)
			for j := range cls.Methods[i].Params {
				substitute(&cls.Methods[i].Params[j].Type, false)
			}
		}
		for i := range cls.Fields {
			substitute(&cls.Fields[i].Type, false)
		}
	}
}

// isTemplateSpecialization returns true if a FunctionDecl node is a template
// specialization (e.g., `template<> T<int>() { ... }`). clang AST marks these
// by including a TemplateArgument child in the inner array.
func isTemplateSpecialization(node *Node) bool {
	if node == nil {
		return false
	}
	for _, child := range node.Inner {
		if strings.Contains(child.Kind, "TemplateArgument") {
			return true
		}
	}
	return false
}

// constructorKind returns "default", "copy", "move", or "other" for a CXXConstructorDecl.
func constructorKind(node *Node) string {
	if node == nil {
		return "other"
	}
	// Check parameter count via inner ParmVarDecl
	var params []*Node
	for i := range node.Inner {
		if node.Inner[i].Kind == "ParmVarDecl" {
			params = append(params, &node.Inner[i])
		}
	}
	// Also fallback to checking type signature
	if len(params) == 0 {
		if node.Type != nil {
			qt := strings.TrimSpace(node.Type.QualType)
			if strings.HasSuffix(qt, "()") || strings.HasSuffix(qt, "(void)") {
				return "default"
			}
		}
		return "default"
	}
	if len(params) == 1 && params[0].Type != nil {
		qt := params[0].Type.QualType
		// Copy ctor: const Foo & or Foo const &
		if strings.Contains(qt, "&&") {
			return "move"
		}
		if strings.HasSuffix(qt, "&") && strings.Contains(qt, node.Name) {
			return "copy"
		}
	}
	return "other"
}

// isDefaultConstructor returns true if the CXXConstructorDecl takes no
// parameters (i.e., it's a default constructor candidate).
func isDefaultConstructor(node *Node) bool {
	return constructorKind(node) == "default"
}

// resolveQualName tries to resolve a possibly unqualified type name to its
// fully qualified name using the known classes map. It tries the name as-is first,
// then prepends the current namespace (and parent namespaces) to find a match.
func (p *Parser) resolveQualName(name, currentNamespace string) string {
	// Already known as fully qualified
	if _, ok := p.classes[name]; ok {
		return name
	}
	// Already contains ::, might be fully qualified but not yet in classes map
	if strings.Contains(name, "::") {
		return name
	}
	// Try current namespace and parent namespaces
	ns := currentNamespace
	for ns != "" {
		candidate := ns + "::" + name
		if _, ok := p.classes[candidate]; ok {
			return candidate
		}
		// Move to parent namespace
		if idx := strings.LastIndex(ns, "::"); idx >= 0 {
			ns = ns[:idx]
		} else {
			break
		}
	}
	return name
}

func (p *Parser) parseFunctionAsMethod(node *Node, className string) *apispec.Function {
	fn := &apispec.Function{
		Name:          node.Name,
		QualName:      className + "::" + node.Name,
		IsVirtual:     node.IsVirtual || node.IsPure,
		IsPureVirtual: node.IsPure,
	}

	// Check if const method from type
	if node.Type != nil && strings.Contains(node.Type.QualType, ") const") {
		fn.IsConst = true
	}
	// Check rvalue ref-qualifier: methods declared with `&&` can only be
	// called on rvalue objects (e.g., `std::move(obj).ToBytes()`). The
	// bridge always holds an lvalue pointer, so such methods are unusable.
	if node.Type != nil && strings.HasSuffix(strings.TrimSpace(node.Type.QualType), "&&") {
		fn.IsRvalueRef = true
	}

	// Check if static
	if node.StorageClass == "static" {
		fn.IsStatic = true
	}

	// Parse return type
	if node.Type != nil {
		fn.ReturnType = p.parseReturnTypeFromNode(node.Type)
	}

	// Parse parameters
	for i := range node.Inner {
		child := &node.Inner[i]
		if child.Kind == "ParmVarDecl" {
			param := apispec.Param{
				Name: child.Name,
			}
			if child.Type != nil {
				param.Type = p.classifyTypeFromNode(child.Type)
			}
			fn.Params = append(fn.Params, param)
		}
	}

	return fn
}

func (p *Parser) parseEnum(node *Node, namespace string) *apispec.Enum {
	e := &apispec.Enum{
		Name:      node.Name,
		Namespace: namespace,
		IsScoped:  node.ScopedEnumTag != "",
	}
	if namespace != "" {
		e.QualName = namespace + "::" + node.Name
	} else {
		e.QualName = node.Name
	}

	for i := range node.Inner {
		child := &node.Inner[i]
		if child.Kind == "EnumConstantDecl" {
			val := apispec.EnumValue{
				Name:    child.Name,
				Comment: extractComment(child),
			}
			// Locate the ConstantExpr that carries the enumerator's
			// integer value. clang wraps the initializer in an
			// ImplicitCastExpr (IntegralCast) when the enum has no
			// fixed underlying type, so ConstantExpr is one level
			// deeper than the EnumConstantDecl's direct children. A
			// recursive walk handles both shapes.
			//
			// When no initializer is present (implicit enumerators
			// like `enum { A, B, C };`), clang emits no ConstantExpr
			// at all; in that case we fall back to "previous value
			// + 1", or 0 for the first entry.
			val.Value = findEnumConstantValue(child, len(e.Values), e.Values)
			e.Values = append(e.Values, val)
		}
	}

	return e
}

// findEnumConstantValue extracts the integer value attached to an
// EnumConstantDecl. clang's JSON dumper places the resolved value on
// a ConstantExpr node nested somewhere inside the EnumConstantDecl's
// inner subtree; the exact depth depends on whether an
// ImplicitCastExpr wrapped the initializer (it does for enums without
// a fixed underlying type) and on `-fparse-all-comments`-induced
// reshaping. When no ConstantExpr is found the enumerator is
// "implicit" (no `= N`); we then auto-increment from the previous
// recorded value, mirroring C++ enumerator semantics.
func findEnumConstantValue(decl *Node, idx int, prior []apispec.EnumValue) int64 {
	if v, ok := searchConstantExpr(decl); ok {
		return v
	}
	if idx == 0 {
		return 0
	}
	return prior[idx-1].Value + 1
}

// searchConstantExpr walks the EnumConstantDecl subtree depth-first
// looking for the first ConstantExpr whose `value` field holds the
// folded integer. Returns the parsed value and true on success.
func searchConstantExpr(n *Node) (int64, bool) {
	if n == nil {
		return 0, false
	}
	if n.Kind == "ConstantExpr" {
		if s := nodeValueString(n); s != "" {
			var out int64
			if _, err := fmt.Sscanf(s, "%d", &out); err == nil {
				return out, true
			}
		}
	}
	for i := range n.Inner {
		if v, ok := searchConstantExpr(&n.Inner[i]); ok {
			return v, true
		}
	}
	return 0, false
}

// parseReturnType extracts the return type from a function type string like "int (const char *, int)".
func (p *Parser) parseReturnType(funcType string) apispec.TypeRef {
	// Function type format: "returnType (paramTypes...)"
	parenIdx := strings.Index(funcType, "(")
	if parenIdx <= 0 {
		return p.classifyType(funcType)
	}
	retType := strings.TrimSpace(funcType[:parenIdx])
	return p.classifyType(retType)
}

// parseReturnTypeFromNode is like parseReturnType but uses the Type node's
// DesugaredQT when the sugared return type is ambiguous. See
// classifyTypeFromNode for the motivation.
func (p *Parser) parseReturnTypeFromNode(t *Type) apispec.TypeRef {
	if t == nil {
		return apispec.TypeRef{}
	}
	ret := p.parseReturnType(t.QualType)
	if t.DesugaredQT == "" || t.DesugaredQT == t.QualType {
		return ret
	}
	// Only substitute when the sugared return resolves to handle/value
	// (likely a type alias to something else) and desugared is primitive/enum.
	if ret.Kind != apispec.TypeHandle && ret.Kind != apispec.TypeValue {
		return ret
	}
	desugared := p.parseReturnType(t.DesugaredQT)
	if desugared.Kind == apispec.TypePrimitive ||
		desugared.Kind == apispec.TypeEnum {
		return desugared
	}
	return ret
}

// classifyTypeFromNode classifies a type from a clang AST Type, preferring
// the desugared qualified type when the sugared type is a simple identifier
// that would be misclassified. This handles class-scoped type aliases such
// as `using Value = intptr_t;` inside a class — the sugared name "Value"
// is indistinguishable from a real class type named Value, but the
// desugared form reveals the true primitive type.
func (p *Parser) classifyTypeFromNode(t *Type) apispec.TypeRef {
	if t == nil {
		return apispec.TypeRef{}
	}
	ref := p.classifyType(t.QualType)
	if t.DesugaredQT == "" || t.DesugaredQT == t.QualType {
		return ref
	}
	// Only switch when the sugared type resolves to a generic "class-ish"
	// kind (handle/value) and the desugared type is a primitive or
	// well-known string/vector. This avoids unnecessary substitutions for
	// cases where the sugared type is already correct (e.g., `std::string`
	// sugared from `std::basic_string<char, ...>`).
	if ref.Kind != apispec.TypeHandle && ref.Kind != apispec.TypeValue {
		return ref
	}
	desugared := p.classifyType(t.DesugaredQT)
	if desugared.Kind == apispec.TypePrimitive ||
		desugared.Kind == apispec.TypeEnum {
		return desugared
	}
	return ref
}

// classifyType determines the TypeKind for a C++ type string.
func (p *Parser) classifyType(qualType string) apispec.TypeRef {
	ref := apispec.TypeRef{
		QualType: qualType,
	}

	// Clean up the type string
	t := strings.TrimSpace(qualType)

	// Check const
	if strings.HasPrefix(t, "const ") {
		ref.IsConst = true
		t = strings.TrimPrefix(t, "const ")
	}
	// Trailing const (e.g., "int *const")
	t = strings.TrimSuffix(t, " const")

	// Check pointer/reference
	if strings.HasSuffix(t, "*") {
		ref.IsPointer = true
		t = strings.TrimSuffix(t, "*")
		t = strings.TrimSpace(t)
	}
	if strings.HasSuffix(t, "&") || strings.HasSuffix(t, "&&") {
		ref.IsRef = true
		t = strings.TrimSuffix(t, "&&")
		t = strings.TrimSuffix(t, "&")
		t = strings.TrimSpace(t)
	}

	// Remove leading const again (for "const std::string &")
	t = strings.TrimPrefix(t, "const ")
	ref.Name = t

	// Namespace-scope typedef expansion is intentionally deferred to
	// postProcessTypedefAliases. Expanding here during the streaming
	// walk would substitute the alias with its raw underlying spelling
	// (e.g. `vector<Type *const>`), in which inner identifiers are
	// unqualified ("Type" instead of "googlesql::Type"). Once the alias
	// name is gone we cannot tell which namespace the typedef was
	// defined in, so we cannot retroactively re-qualify those tokens.
	// Leaving ref.Name = alias keeps that information intact;
	// postProcessTypedefAliases then re-qualifies inner identifiers
	// against the full p.classes map and the typedef's own namespace
	// before substituting in the underlying type.

	// Classify
	switch {
	case t == "void":
		ref.Kind = apispec.TypeVoid

	case t == "bool":
		ref.Kind = apispec.TypePrimitive

	// char* / const char* is a C-style string, not a pointer to primitive
	case t == "char" && ref.IsPointer:
		ref.Kind = apispec.TypeString
		ref.IsPointer = false // strings are handled as values in the bridge

	case isPrimitiveType(t):
		ref.Kind = apispec.TypePrimitive

	case isStringType(t):
		ref.Kind = apispec.TypeString

	case (strings.HasPrefix(t, "std::vector<") || strings.HasPrefix(t, "vector<")) &&
		!strings.Contains(t, "::iterator") && !strings.Contains(t, "::const_iterator"):
		ref.Kind = apispec.TypeVector
		inner := extractTemplateArg(t)
		if inner != "" {
			innerRef := p.classifyType(inner)
			ref.Inner = &innerRef
		}

	case strings.HasPrefix(t, "std::unique_ptr<") || strings.HasPrefix(t, "unique_ptr<") ||
		strings.HasPrefix(t, "std::shared_ptr<") || strings.HasPrefix(t, "shared_ptr<"):
		// Smart pointers are handle types (ownership-wrapping pointers)
		ref.Kind = apispec.TypeHandle

	default:
		// If pointer or reference to a class, it's likely a handle
		if ref.IsPointer || ref.IsRef {
			ref.Kind = apispec.TypeHandle
		} else {
			// Could be value type, enum, or unknown class
			ref.Kind = apispec.TypeValue
		}
	}

	return ref
}

func isPrimitiveType(t string) bool {
	primitives := map[string]bool{
		"int": true, "unsigned int": true,
		"long": true, "unsigned long": true,
		"long long": true, "unsigned long long": true,
		"short": true, "unsigned short": true,
		"char": true, "unsigned char": true, "signed char": true,
		"float": true, "double": true, "long double": true,
		"int8_t": true, "int16_t": true, "int32_t": true, "int64_t": true,
		"uint8_t": true, "uint16_t": true, "uint32_t": true, "uint64_t": true,
		"size_t": true, "ssize_t": true, "ptrdiff_t": true,
		"intptr_t": true, "uintptr_t": true,
	}
	return primitives[t]
}

func isStringType(t string) bool {
	// Recognises the standard-library string shapes. Library-specific
	// string aliases (absl::string_view, absl::Cord, …) are routed
	// through BridgeConfig.ExtraStringTypes at the generator layer so
	// the parser stays library-agnostic.
	stringTypes := map[string]bool{
		"std::string":                  true,
		"std::basic_string<char>":      true,
		"string":                       true,
		"std::string_view":             true,
		"std::basic_string_view<char>": true,
		"char *":                       true,
		"char":                         true, // const char * after stripping
	}
	return stringTypes[t]
}

func extractTemplateArg(t string) string {
	start := strings.Index(t, "<")
	if start < 0 {
		return ""
	}
	end := strings.LastIndex(t, ">")
	if end <= start {
		return ""
	}
	return strings.TrimSpace(t[start+1 : end])
}

func cleanTypeName(t string) string {
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "class ")
	t = strings.TrimPrefix(t, "struct ")
	return t
}

// nodeValueString extracts a string from a Node's Value field (json.RawMessage).
// The value can be a JSON string or a JSON number/bool.
func nodeValueString(node *Node) string {
	if len(node.Value) == 0 {
		return ""
	}
	// Try as JSON string first
	var s string
	if err := json.Unmarshal(node.Value, &s); err == nil {
		return s
	}
	// Fall back to raw string (e.g., number or bool literal)
	return strings.Trim(string(node.Value), " \t\n\r\"")
}

// hasSysrootFlag checks if -isysroot or --sysroot is already in the flags.
func hasSysrootFlag(flags []string) bool {
	for _, f := range flags {
		if strings.HasPrefix(f, "-isysroot") || strings.HasPrefix(f, "--sysroot") {
			return true
		}
	}
	return false
}

// detectMacOSSDKFlags returns -isysroot flags for macOS if xcrun is available.
func detectMacOSSDKFlags() []string {
	out, err := exec.Command("xcrun", "--show-sdk-path").Output()
	if err != nil {
		return nil
	}
	sdkPath := strings.TrimSpace(string(out))
	if sdkPath == "" {
		return nil
	}

	flags := []string{"-isysroot", sdkPath}

	// Check if C++ headers are in SDK (some macOS setups need this)
	cxxInclude := sdkPath + "/usr/include/c++/v1"
	if info, err := os.Stat(cxxInclude); err == nil && info.IsDir() {
		flags = append(flags, "-I"+cxxInclude)
	}

	return flags
}

// CollectCompileFlags extracts -I, -D, -std= flags from build step args.
func CollectCompileFlags(args []string) []string {
	var flags []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case strings.HasPrefix(arg, "-I"),
			strings.HasPrefix(arg, "-D"),
			strings.HasPrefix(arg, "-std="),
			strings.HasPrefix(arg, "-isystem"),
			strings.HasPrefix(arg, "-iquote"):
			flags = append(flags, arg)
			// Handle separate argument form
			if (arg == "-I" || arg == "-D" || arg == "-isystem" || arg == "-iquote") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		}
	}
	return flags
}

// normalizeSourceFile strips the bazel sandbox prefix from paths produced by
// clang's location info so api-spec.json records portable paths. Bazel mounts
// every build action under a per-invocation execroot that includes the
// developer's $HOME-adjacent tmp dir:
//
//	/private/var/tmp/_bazel_goccy/<hash>/execroot/_main/googlesql/public/foo.h
//
// That path is meaningless on CI. What we want is the workspace-relative
// remainder ("googlesql/public/foo.h" or "bazel-out/.../generated.pb.h"),
// which is stable across machines and matches the form build.log uses.
//
// Relative paths are returned unchanged; non-bazel absolute paths fall
// through (callers can further relativize when they have the relevant root).
func normalizeSourceFile(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") {
		return p
	}
	if idx := strings.Index(p, "/execroot/"); idx >= 0 {
		rest := p[idx+len("/execroot/"):]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			return rest[slash+1:]
		}
	}
	return p
}
