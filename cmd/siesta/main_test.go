package main

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestModeFromFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"long equals", []string{"--mode=fg"}, "fg"},
		{"short equals", []string{"-mode=cli"}, "cli"},
		{"long space", []string{"--mode", "fg"}, "fg"},
		{"short space", []string{"-mode", "cli"}, "cli"},
		{"among others", []string{"--cluster", "c", "--mode=fg", "--count", "1"}, "fg"},
		{"trailing flag no value", []string{"--mode"}, ""},
		{"absent", []string{"--cluster", "c"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modeFromFlag(tt.args); got != tt.want {
				t.Fatalf("modeFromFlag(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func TestResolveMode(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		getenv map[string]string
		want   string
	}{
		{"flag wins", []string{"--mode=fg"}, map[string]string{"SIESTA_MODE": "cli", "RUNTIME_API_ADDR": "x"}, "fg"},
		{"env over autodetect", nil, map[string]string{"SIESTA_MODE": "cli", "RUNTIME_API_ADDR": "x"}, "cli"},
		{"autodetect fg", nil, map[string]string{"RUNTIME_API_ADDR": "127.0.0.1:9000"}, "fg"},
		{"default cli", nil, map[string]string{}, "cli"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveMode(tt.args, env(tt.getenv)); got != tt.want {
				t.Fatalf("resolveMode = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDispatch_UnknownMode(t *testing.T) {
	code := dispatch([]string{"--mode=bogus"}, env(nil))
	if code != 2 {
		t.Fatalf("expected exit 2 for unknown mode, got %d", code)
	}
}
