package state_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkastordev/kastor/internal/state"
	"github.com/google/go-cmp/cmp"
)

func TestBindMigrationPreservesResources(t *testing.T) {
	f := sample()
	f.Version = 1
	ts := f.Targets["openai_assistants"]
	ts.Plugin = nil
	before, _ := json.Marshal(ts.Resources)
	owner := state.PluginIdentity{Source: "github.com/example/plugin", Version: "0.2.1", Protocol: 1}
	if err := f.Bind(map[string]state.PluginIdentity{"openai_assistants": owner}); err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(ts.Resources)
	if string(before) != string(after) || f.Serial != 6 || f.Version != 1 {
		t.Fatal("binding changed resources, serial, or persisted format version")
	}
	a, b := t.TempDir(), t.TempDir()
	if err := f.Write(a); err != nil {
		t.Fatal(err)
	}
	f.Serial--
	if err := f.Write(b); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(string(readState(t, a)), string(readState(t, b))); diff != "" {
		t.Fatal(diff)
	}
	loaded, err := state.Load(a)
	if err != nil || loaded.Version != 2 || *loaded.Targets["openai_assistants"].Plugin != owner {
		t.Fatalf("migrated state = %+v, %v", loaded, err)
	}
}

func TestBindRefusesUnresolvedOrChangedOwnership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
		owners  map[string]state.PluginIdentity
		want    string
	}{
		{"unresolved v1", 1, nil, "unresolved plugin ownership"},
		{"source switch", 2, map[string]state.PluginIdentity{"openai_assistants": {Source: "other/plugin", Version: "0.1.0", Protocol: 1}}, "does not match managed owner"},
		{"future format", 3, nil, "unsupported state version"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := sample()
			f.Version = tc.version
			before, _ := json.Marshal(f)
			if err := f.Bind(tc.owners); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Bind error = %v, want %s", err, tc.want)
			}
			after, _ := json.Marshal(f)
			if string(before) != string(after) {
				t.Fatal("failed binding modified state")
			}
		})
	}
}

func TestWriteRefusesUnmigratedAndFutureState(t *testing.T) {
	for _, version := range []int{1, 3} {
		f := sample()
		f.Version = version
		f.Targets["openai_assistants"].Plugin = nil
		dir := t.TempDir()
		if err := f.Write(dir); err == nil {
			t.Fatal("Write accepted unresolved/future state")
		}
		if f.Serial != 6 || f.Version != version {
			t.Fatal("failed Write changed serial/version")
		}
		if _, err := os.Stat(filepath.Join(dir, state.Filename)); !os.IsNotExist(err) {
			t.Fatal("failed Write created a snapshot")
		}
	}
}

func TestWriteFailurePreservesSnapshotAndSerial(t *testing.T) {
	dir := t.TempDir()
	f := sample()
	if err := f.Write(dir); err != nil {
		t.Fatal(err)
	}
	before := readState(t, dir)
	// Force an IO failure independent of permissions (including root runners).
	if err := os.Mkdir(filepath.Join(dir, state.Filename+".tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.Write(dir); err == nil {
		t.Fatal("Write succeeded")
	}
	if f.Serial != 7 || string(readState(t, dir)) != string(before) {
		t.Fatal("failed Write changed the committed snapshot or serial")
	}
}
