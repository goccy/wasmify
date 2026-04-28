package tools

// Catalog returns the default install recipes for common build tools. Project
// analysis records resolved Tool values (copied from the catalog) into
// arch.json so CI can install the same set without re-detecting.
func Catalog() map[string]Tool {
	apt := func(pkg string) InstallSpec {
		return InstallSpec{Commands: []string{
			sudoPrefix() + "apt-get update",
			sudoPrefix() + "apt-get install -y " + pkg,
		}}
	}
	brew := func(pkg string) InstallSpec {
		return InstallSpec{Commands: []string{"brew install " + pkg}}
	}
	simple := func(name, aptPkg, brewPkg string) Tool {
		return Tool{
			Name: name,
			Install: map[OS]InstallSpec{
				OSDebian: apt(aptPkg),
				OSDarwin: brew(brewPkg),
			},
		}
	}

	m := map[string]Tool{
		"cmake":      simple("cmake", "cmake", "cmake"),
		"ninja":      simple("ninja", "ninja-build", "ninja"),
		"make":       simple("make", "build-essential", "make"),
		"autoconf":   simple("autoconf", "autoconf", "autoconf"),
		"automake":   simple("automake", "automake", "automake"),
		"libtool":    simple("libtool", "libtool", "libtool"),
		"pkg-config": simple("pkg-config", "pkg-config", "pkg-config"),
		"meson":      simple("meson", "meson", "meson"),
		"python3":    simple("python3", "python3", "python@3"),
		"clang":      simple("clang", "clang", "llvm"),
		"git":        simple("git", "git", "git"),
	}
	m["bazel"] = Tool{
		Name:      "bazel",
		DetectCmd: "command -v bazel || command -v bazelisk",
		Install: map[OS]InstallSpec{
			OSDarwin: brew("bazelisk"),
			OSDebian: {Commands: []string{
				"curl -fsSL https://github.com/bazelbuild/bazelisk/releases/latest/download/bazelisk-linux-amd64 -o /tmp/bazelisk",
				"chmod +x /tmp/bazelisk",
				sudoPrefix() + "mv /tmp/bazelisk /usr/local/bin/bazel",
			}},
		},
	}
	return m
}

// Lookup returns a copy of the catalog entry with the given name and true, or
// a zero Tool and false if not found.
func Lookup(name string) (Tool, bool) {
	t, ok := Catalog()[name]
	return t, ok
}
