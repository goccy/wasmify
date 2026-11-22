package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The capture build's compiles get the host-include directory and the
// macros of the capabilities wasmify.json gives, nothing without any, and
// an error for a tree given with a deprecated flag.
func TestHostCaptureInjectFlags(t *testing.T) {
	for _, v := range []string{"WASMIFY_HOST_SOCKETS", "WASMIFY_HOST_SUBPROCESS", "WASMIFY_HOST_FS", "WASMIFY_HOST_THREADS"} {
		t.Setenv(v, "")
	}
	project := func(t *testing.T, text string) (string, string) {
		t.Helper()
		outDir, dataDir := t.TempDir(), t.TempDir()
		if err := os.WriteFile(filepath.Join(outDir, "wasmify.json"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		return dataDir, outDir
	}

	dataDir, outDir := project(t, `{"version": "1", "host": {"network": {"sockets": true}}}`)
	flags, err := hostCaptureInjectFlags(dataDir, outDir)
	if err != nil {
		t.Fatal(err)
	}
	incDir := filepath.Join(dataDir, "wasm-build", "host-include")
	for _, want := range []string{"-isystem " + incDir, "-DWASMIFY_HOST_NETWORK_SOCKETS", "-DWASMIFY_HOST_SOCKETS"} {
		if !strings.Contains(flags, want) {
			t.Errorf("flags %q lack %q", flags, want)
		}
	}
	if strings.Contains(flags, "-pthread") || strings.Contains(flags, "WASMIFY_HOST_PROCESS") {
		t.Errorf("flags %q give more than network.sockets", flags)
	}
	if _, err := os.Stat(filepath.Join(incDir, "netdb.h")); err != nil {
		t.Errorf("netdb.h not deployed for the capture: %v", err)
	}

	for _, text := range []string{
		`{"version": "1", "host": {"network": true}, "bridge": {"HostSockets": true}}`,
		`{"version": "1", "host": {}, "bridge": {"HostSockets": true}}`,
		`{"version": "1", "host": {"network": true}, "bridge": {"HostFS": true}}`,
	} {
		dataDir, outDir = project(t, text)
		if _, err := hostCaptureInjectFlags(dataDir, outDir); err == nil {
			t.Errorf("%s: a tree with a deprecated flag should be refused", text)
		}
	}

	dataDir, outDir = project(t, `{"version": "1"}`)
	if flags, err := hostCaptureInjectFlags(dataDir, outDir); err != nil || flags != "" {
		t.Errorf("no capability: %q, %v; want nothing", flags, err)
	}

	// The deprecated flags inject what they did before the tree: nothing
	// for HostFS, no host-include directory for HostThreads, and nothing
	// for their environment variables.
	dataDir, outDir = project(t, `{"version": "1", "bridge": {"HostFS": true}}`)
	if flags, err := hostCaptureInjectFlags(dataDir, outDir); err != nil || flags != "" {
		t.Errorf("HostFS: %q, %v; want nothing", flags, err)
	}
	dataDir, outDir = project(t, `{"version": "1", "bridge": {"HostThreads": true}}`)
	flags, err = hostCaptureInjectFlags(dataDir, outDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(flags, "-pthread") || strings.Contains(flags, "host-include") {
		t.Errorf("HostThreads: %q, want -pthread and no host-include directory", flags)
	}
	t.Setenv("WASMIFY_HOST_SOCKETS", "1")
	dataDir, outDir = project(t, `{"version": "1"}`)
	if flags, err := hostCaptureInjectFlags(dataDir, outDir); err != nil || flags != "" {
		t.Errorf("WASMIFY_HOST_SOCKETS alone: %q, %v; want nothing", flags, err)
	}
}
