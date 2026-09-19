package state_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/getkastordev/kastor/internal/state"
)

// sample returns a populated state file exercising every schema field:
// multiple targets, multiple resources, dependencies present and absent.
func sample() *state.File {
	return &state.File{
		Version: state.Version,
		Serial:  6,
		Targets: map[string]*state.TargetState{
			"openai_assistants": {
				Plugin: &state.PluginIdentity{Source: "example/test", Version: "0.1.0", Protocol: 1},
				Resources: map[string]*state.Resource{
					"agent.weather": {
						ID:           "asst_123",
						Config:       json.RawMessage(`{"description":"Answers weather questions","model":{"id":"gpt-4o-mini","provider":"openai"}}`),
						Dependencies: []string{"agent.geocoder"},
					},
					"agent.geocoder": {
						ID:     "asst_456",
						Config: json.RawMessage(`{"model":{"id":"gpt-4o-mini","provider":"openai"}}`),
					},
				},
			},
			"empty_platform": {Resources: map[string]*state.Resource{}},
		},
	}
}

func TestLoadMissingFileIsEmptyState(t *testing.T) {
	f, err := state.Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: unexpected error: %v", err)
	}
	want := &state.File{Version: state.Version, Targets: map[string]*state.TargetState{}}
	if diff := cmp.Diff(want, f); diff != "" {
		t.Errorf("Load() (-want +got):\n%s", diff)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name    string
		dir     string
		wantSub []string // substrings the error must contain
	}{
		{
			name:    "corrupt JSON names the file",
			dir:     "testdata/corrupt",
			wantSub: []string{state.Filename, "parsing state file"},
		},
		{
			name:    "future version is rejected, naming found and supported",
			dir:     "testdata/future_version",
			wantSub: []string{state.Filename, "version 3", "supports versions 1 and 2"},
		},
		{
			name:    "missing version is rejected",
			dir:     "testdata/version_missing",
			wantSub: []string{state.Filename, "version 0", "supports versions 1 and 2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := state.Load(tt.dir)
			if err == nil {
				t.Fatal("Load succeeded, want error")
			}
			for _, want := range tt.wantSub {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing substring %q", err, want)
				}
			}
		})
	}
}

func TestWriteLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	f := sample()
	if err := f.Write(dir); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := state.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := sample()
	want.Serial = 7                        // Write bumps the serial
	delete(want.Targets, "empty_platform") // Write drops empty targets
	if diff := cmp.Diff(want, got, cmp.Transformer("compact", compactJSON)); diff != "" {
		t.Errorf("round trip (-want +got):\n%s", diff)
	}
}

// compactJSON normalizes raw config whitespace so round-trip comparison is
// about content, not indentation.
func compactJSON(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(raw)
	}
	return string(out)
}

func TestWriteIsDeterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := sample().Write(a); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := sample().Write(b); err != nil {
		t.Fatalf("Write: %v", err)
	}

	bytesA := readState(t, a)
	bytesB := readState(t, b)
	if string(bytesA) != string(bytesB) {
		t.Errorf("two writes of equal state differ:\n%s\nvs:\n%s", bytesA, bytesB)
	}
}

func TestWriteGolden(t *testing.T) {
	dir := t.TempDir()
	if err := sample().Write(dir); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := readState(t, dir)

	want, err := os.ReadFile(filepath.Join("testdata", "golden.state.json"))
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}
	if diff := cmp.Diff(string(want), string(got)); diff != "" {
		t.Errorf("state bytes (-want +got):\n%s", diff)
	}
}

func TestWriteEndsWithNewline(t *testing.T) {
	dir := t.TempDir()
	if err := sample().Write(dir); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := readState(t, dir)
	if len(got) == 0 || got[len(got)-1] != '\n' {
		t.Error("state file does not end with a newline")
	}
}

func TestWriteBumpsSerialEachTime(t *testing.T) {
	dir := t.TempDir()
	f := sample()
	for i := 1; i <= 3; i++ {
		if err := f.Write(dir); err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
	}
	got, err := state.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Serial != 9 {
		t.Errorf("Serial = %d, want 9 (6 + one per write)", got.Serial)
	}
}

func TestTargetGetOrCreate(t *testing.T) {
	f := &state.File{Version: state.Version, Targets: map[string]*state.TargetState{}}
	ts := f.Target("openai_assistants")
	if ts == nil || ts.Resources == nil {
		t.Fatal("Target did not initialize the target state")
	}
	ts.Resources["agent.a"] = &state.Resource{ID: "x", Config: json.RawMessage(`{}`)}
	if again := f.Target("openai_assistants"); again != ts {
		t.Error("Target returned a new value for an existing target")
	}
}

func readState(t *testing.T, dir string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, state.Filename))
	if err != nil {
		t.Fatalf("reading state file: %v", err)
	}
	return data
}
