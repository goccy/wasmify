package main

import "testing"

// TestPluginConfig covers plugin-parameter parsing and validation for
// the runtime backend selection. It exercises pluginConfig directly
// with synthetic key/value pairs — no proto input, no project.
func TestPluginConfig(t *testing.T) {
	t.Run("default runtime is wazero", func(t *testing.T) {
		var c pluginConfig
		if c.wasm2goRuntime() {
			t.Error("zero-value config should not select the wasm2go runtime")
		}
		if err := c.validate(); err != nil {
			t.Errorf("default config should validate: %v", err)
		}
	})

	t.Run("runtime=wasm2go with wasm path", func(t *testing.T) {
		var c pluginConfig
		if err := c.set("runtime", "wasm2go"); err != nil {
			t.Fatal(err)
		}
		if err := c.set("wasm", "./build/lib.wasm"); err != nil {
			t.Fatal(err)
		}
		if !c.wasm2goRuntime() {
			t.Error("runtime=wasm2go not recorded")
		}
		if c.wasm != "./build/lib.wasm" {
			t.Errorf("wasm path = %q, want ./build/lib.wasm", c.wasm)
		}
		if err := c.validate(); err != nil {
			t.Errorf("valid wasm2go config rejected: %v", err)
		}
	})

	t.Run("runtime=wasm2go without wasm path fails validation", func(t *testing.T) {
		var c pluginConfig
		if err := c.set("runtime", "wasm2go"); err != nil {
			t.Fatal(err)
		}
		if err := c.validate(); err == nil {
			t.Error("runtime=wasm2go without wasm= should fail validation")
		}
	})

	t.Run("explicit runtime=wazero", func(t *testing.T) {
		var c pluginConfig
		if err := c.set("runtime", "wazero"); err != nil {
			t.Fatal(err)
		}
		if c.wasm2goRuntime() {
			t.Error("runtime=wazero must not select wasm2go")
		}
		if err := c.validate(); err != nil {
			t.Errorf("wazero config should validate: %v", err)
		}
	})

	t.Run("unknown runtime value is rejected", func(t *testing.T) {
		var c pluginConfig
		if err := c.set("runtime", "nodejs"); err == nil {
			t.Error("unknown runtime value should be rejected")
		}
	})

	t.Run("unknown parameter name is rejected", func(t *testing.T) {
		var c pluginConfig
		if err := c.set("frobnicate", "1"); err == nil {
			t.Error("unknown parameter name should be rejected")
		}
	})
}
