package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pluginruntime "github.com/getkastordev/kastor/internal/plugin"
	"github.com/getkastordev/kastor/internal/provider"
	"github.com/getkastordev/kastor/internal/provider/claude"
	"github.com/getkastordev/kastor/internal/provider/providertest"
	"github.com/getkastordev/kastor/internal/schema"
	"github.com/getkastordev/kastor/internal/state"
	protocol "github.com/getkastordev/kastor/protocol/v1"
)

// Exercise the actual external-platform adapter with a persistent fake remote,
// including read/diff, partial failures, and reverse-dependency destruction.
type statePluginClient struct {
	*fakePluginClient
	remote     *providertest.Fake
	comparison provider.Provider
}

func (c *statePluginClient) Read(ctx context.Context, r *protocol.ReadRequest) (*protocol.ReadResponse, error) {
	obj, found, err := c.remote.Read(ctx, r.ID)
	return &protocol.ReadResponse{Remote: obj, Found: found}, err
}
func (c *statePluginClient) Create(ctx context.Context, r *protocol.CreateRequest) (*protocol.CreateResponse, error) {
	id, err := c.remote.Create(ctx, &provider.Resource{Addr: r.Desired.Addr, Config: r.Desired.Config})
	return &protocol.CreateResponse{ID: id}, err
}
func (c *statePluginClient) Update(ctx context.Context, r *protocol.UpdateRequest) error {
	return c.remote.Update(ctx, r.ID, &provider.Resource{Addr: r.Desired.Addr, Config: r.Desired.Config})
}
func (c *statePluginClient) Delete(ctx context.Context, r *protocol.DeleteRequest) error {
	return c.remote.Delete(ctx, r.ID)
}
func (c *statePluginClient) Diff(_ context.Context, r *protocol.DiffRequest) (*protocol.DiffResponse, error) {
	var comparison provider.Provider = c.remote
	if c.comparison != nil {
		comparison = c.comparison
	}
	diffs, err := comparison.Diff(&provider.Resource{Addr: r.Desired.Addr, Config: r.Desired.Config}, r.Remote)
	out := &protocol.DiffResponse{}
	for _, d := range diffs {
		out.Diffs = append(out.Diffs, protocol.AttrDiff{Path: d.Path, Old: d.Old, New: d.New})
	}
	return out, err
}

func TestStateV02ClaudeNormalizedFixtureMigration(t *testing.T) {
	_, c := migrationModule(t)
	// Replace the seeded module with the actual legacy normalized state shape.
	fixture := copyModule(t, "testdata/state_v1_claude")
	data, err := os.ReadFile("testdata/state_v1_claude/kastor.state.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, state.Filename), data, 0o644); err != nil {
		t.Fatal(err)
	}
	c.comparison = claude.New()
	st, err := state.Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	res := st.Targets["claude_agents"].Resources["agent.minimal"]
	var remote provider.Object
	if err := json.Unmarshal(res.Config, &remote); err != nil {
		t.Fatal(err)
	}
	c.remote.Objects = map[string]provider.Object{res.ID: remote}
	c.remote.Calls = nil
	before := readStateFile(t, fixture)
	if out, err := runCLI(t, "plan", fixture); err != nil || !strings.Contains(out, "No changes") {
		t.Fatalf("Claude fixture plan: %v\n%s", err, out)
	}
	if readStateFile(t, fixture) != before {
		t.Fatal("plan persisted migration")
	}
	if out, err := runCLI(t, "apply", fixture); err != nil {
		t.Fatalf("Claude fixture migration: %v\n%s", err, out)
	}
	assertNoRemoteMutations(t, c)
	got, err := state.Load(fixture)
	if err != nil {
		t.Fatal(err)
	}
	oldResources, _ := json.Marshal(st.Targets["claude_agents"].Resources)
	newResources, _ := json.Marshal(got.Targets["claude_agents"].Resources)
	if string(oldResources) != string(newResources) || got.Version != 2 || got.Serial != 5 {
		t.Fatalf("legacy normalized config was not preserved: %s", readStateFile(t, fixture))
	}
}

func migrationModule(t *testing.T) (string, *statePluginClient) {
	t.Helper()
	dir := copyModule(t, "testdata/platform")
	writeMigrationModule(t, dir, claudePluginSource, "anthropic")
	c := &statePluginClient{fakePluginClient: newFakePluginClient(claudePluginSource, protocol.KindPlatform), remote: providertest.New()}
	c.metadata.Capabilities.Check = true
	previous := openPlugin
	openPlugin = func(_ context.Context, _, _ string, req *schema.PluginRequirement) (pluginruntime.Client, error) {
		if req.Source != c.metadata.Source {
			return nil, fmt.Errorf("unexpected source %s", req.Source)
		}
		return c, nil
	}
	t.Cleanup(func() { openPlugin = previous })
	if out, err := runCLI(t, "apply", dir); err != nil {
		t.Fatalf("seed: %v\n%s", err, out)
	}
	c.remote.Calls = nil
	return dir, c
}

func writeMigrationModule(t *testing.T, dir, source, alias string) {
	t.Helper()
	body := fmt.Sprintf(`kastor {
  required_plugins {
    %s = {
      source = %q
      version = "~> 0.1"
    }
  }
}
model "fast" {
  provider = "anthropic"
  id = "claude-haiku-4-5"
}
target "claude_agents" {
  type = "platform"
  plugin = %q
}
`, alias, source, alias)
	if err := os.WriteFile(filepath.Join(dir, "kastor.hcl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rewriteState(t *testing.T, dir string, edit func(*state.File)) *state.File {
	t.Helper()
	f, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	edit(f)
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, state.Filename), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

func downgradeState(t *testing.T, dir string) *state.File {
	t.Helper()
	return rewriteState(t, dir, func(f *state.File) {
		f.Version = 1
		for _, ts := range f.Targets {
			ts.Plugin = nil
		}
	})
}

func assertNoRemoteMutations(t *testing.T, c *statePluginClient) {
	t.Helper()
	for _, call := range c.remote.Calls {
		if strings.HasPrefix(call, "create ") || strings.HasPrefix(call, "update ") || strings.HasPrefix(call, "delete ") {
			t.Fatalf("unexpected remote mutation: %s", call)
		}
	}
}

func TestStateV1MigrationNoChurnAndReadOnlyCommands(t *testing.T) {
	dir, c := migrationModule(t)
	old := downgradeState(t, dir)
	before := readStateFile(t, dir)
	for _, cmd := range []string{"plan", "doctor"} {
		out, err := runCLI(t, cmd, dir)
		if err != nil {
			t.Fatalf("%s: %v\n%s", cmd, err, out)
		}
		if cmd == "plan" && !strings.Contains(out, "No changes") {
			t.Fatalf("migration introduced churn: %s", out)
		}
		if readStateFile(t, dir) != before {
			t.Fatalf("%s persisted migration", cmd)
		}
	}
	if out, err := runCLI(t, "apply", dir); err != nil {
		t.Fatalf("migrate: %v\n%s", err, out)
	}
	assertNoRemoteMutations(t, c)
	f, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner := f.Targets["claude_agents"].Plugin
	if f.Version != 2 || f.Serial != old.Serial+1 || owner.Source != claudePluginSource || owner.Version != "0.1.0" || owner.Protocol != 1 {
		t.Fatalf("wrong migrated metadata: %+v / %+v", f, owner)
	}
	want, _ := json.Marshal(old.Targets["claude_agents"].Resources)
	got, _ := json.Marshal(f.Targets["claude_agents"].Resources)
	if string(want) != string(got) {
		t.Fatal("migration changed resource IDs/config/dependencies")
	}
	before = readStateFile(t, dir)
	if _, err := runCLI(t, "apply", dir); err != nil {
		t.Fatal(err)
	}
	if readStateFile(t, dir) != before {
		t.Fatal("second no-op apply rewrote state")
	}
}

func TestStatePluginVersionUpgradeAndAliasRename(t *testing.T) {
	dir, c := migrationModule(t)
	c.metadata.Version = "0.1.1"
	writeMigrationModule(t, dir, claudePluginSource, "renamed")
	before := readStateFile(t, dir)
	if out, err := runCLI(t, "plan", dir); err != nil || !strings.Contains(out, "No changes") {
		t.Fatalf("version upgrade plan: %v\n%s", err, out)
	}
	if readStateFile(t, dir) != before {
		t.Fatal("plan persisted version upgrade")
	}
	if _, err := runCLI(t, "apply", dir); err != nil {
		t.Fatal(err)
	}
	f, _ := state.Load(dir)
	if f.Targets["claude_agents"].Plugin.Version != "0.1.1" {
		t.Fatal("new resolved version not recorded")
	}
	assertNoRemoteMutations(t, c)
}

func TestStateRefusesSourceSwitchBeforeLifecycleCalls(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, cmd := range []string{"plan", "doctor", "apply", "destroy"} {
			t.Run(fmt.Sprintf("v%d/%s", version, cmd), func(t *testing.T) {
				dir, c := migrationModule(t)
				if version == 1 {
					downgradeState(t, dir)
				}
				before := readStateFile(t, dir)
				c.metadata.Source = "github.com/example/other"
				writeMigrationModule(t, dir, c.metadata.Source, "anthropic")
				if out, err := runCLI(t, cmd, dir); err == nil || !strings.Contains(out, "does not match") {
					t.Fatalf("%s accepted source switch: %v\n%s", cmd, err, out)
				}
				if len(c.remote.Calls) != 0 || readStateFile(t, dir) != before {
					t.Fatal("source mismatch reached lifecycle or changed state")
				}
			})
		}
	}
}

func TestStateMigrationPartialFailureRecoveryAndDestroy(t *testing.T) {
	for _, failFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(failFirst), func(t *testing.T) {
			dir, c := migrationModule(t)
			old := downgradeState(t, dir)
			before := readStateFile(t, dir)
			for _, obj := range c.remote.Objects {
				obj["description"] = "drift"
			}
			failed := "agent.weather"
			if failFirst {
				failed = "agent.geocoder"
			}
			c.remote.FailOn = map[string]error{"update " + failed: errors.New("injected failure")}
			if _, err := runCLI(t, "apply", dir); err == nil {
				t.Fatal("expected partial failure")
			}
			f, err := state.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if failFirst {
				if readStateFile(t, dir) != before {
					t.Fatal("first-operation failure persisted migration")
				}
			} else if f.Version != 2 || f.Serial != old.Serial+1 || f.Targets["claude_agents"].Plugin == nil {
				t.Fatalf("successful prefix not saved as v2: %+v", f)
			}
			c.remote.FailOn = nil
			if out, err := runCLI(t, "apply", dir); err != nil {
				t.Fatalf("recovery: %v\n%s", err, out)
			}
			// Destroy directly from v1 also migrates each successful deletion.
			downgradeState(t, dir)
			c.remote.Calls = nil
			if out, err := runCLI(t, "destroy", dir); err != nil {
				t.Fatalf("destroy: %v\n%s", err, out)
			}
			f, err = state.Load(dir)
			if err != nil || f.Version != 2 || len(f.Targets) != 0 || len(c.remote.Objects) != 0 {
				t.Fatalf("destroy state: %+v, %v", f, err)
			}
			weather := old.Targets["claude_agents"].Resources["agent.weather"].ID
			geocoder := old.Targets["claude_agents"].Resources["agent.geocoder"].ID
			if strings.Join(c.remote.Calls, ",") != "delete "+weather+",delete "+geocoder {
				t.Fatalf("delete order: %v", c.remote.Calls)
			}
		})
	}
}

func TestStateMigrationAmbiguousAndFutureFormats(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*state.File)
		want string
	}{
		{"missing target", func(f *state.File) {
			f.Version = 1
			f.Targets["removed"] = f.Targets["claude_agents"]
			delete(f.Targets, "claude_agents")
		}, "restore its original target"},
		{"future version", func(f *state.File) { f.Version = 3 }, "supports versions 1 and 2"},
		{"missing v2 identity", func(f *state.File) { f.Targets["claude_agents"].Plugin = nil }, "invalid target ownership"},
		{"missing protocol", func(f *state.File) { f.Targets["claude_agents"].Plugin.Protocol = 0 }, "invalid target ownership"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, c := migrationModule(t)
			rewriteState(t, dir, tc.edit)
			before := readStateFile(t, dir)
			if out, err := runCLI(t, "apply", dir); err == nil || !strings.Contains(out, tc.want) {
				t.Fatalf("error: %v\n%s", err, out)
			}
			if readStateFile(t, dir) != before || len(c.remote.Calls) != 0 {
				t.Fatal("invalid state mutated or reached remote")
			}
		})
	}
}

func TestStateMigrationRefreshesGenuinelyStaleConfig(t *testing.T) {
	dir, c := migrationModule(t)
	downgradeState(t, dir)
	rewriteState(t, dir, func(f *state.File) {
		res := f.Targets["claude_agents"].Resources["agent.geocoder"]
		var config provider.Object
		if err := json.Unmarshal(res.Config, &config); err != nil {
			t.Fatal(err)
		}
		config["description"] = "old config"
		res.Config, _ = json.Marshal(config)
	})
	if out, err := runCLI(t, "apply", dir); err != nil {
		t.Fatalf("refresh: %v\n%s", err, out)
	}
	assertNoRemoteMutations(t, c)
	f, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 2 || strings.Contains(string(f.Targets["claude_agents"].Resources["agent.geocoder"].Config), "old config") {
		t.Fatal("migration failed to refresh stale config")
	}
	if out, err := runCLI(t, "plan", dir); err != nil || strings.Contains(out, "Warning:") {
		t.Fatalf("drift recurred: %v\n%s", err, out)
	}
}

func TestStateMigrationPartialDestroy(t *testing.T) {
	dir, c := migrationModule(t)
	old := downgradeState(t, dir)
	id := old.Targets["claude_agents"].Resources["agent.geocoder"].ID
	c.remote.FailOn = map[string]error{"delete " + id: errors.New("injected failure")}
	if _, err := runCLI(t, "destroy", dir); err == nil {
		t.Fatal("expected deletion failure")
	}
	f, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	ts := f.Targets["claude_agents"]
	if f.Version != 2 || f.Serial != old.Serial+1 || ts.Plugin.Source != claudePluginSource || len(ts.Resources) != 1 || ts.Resources["agent.geocoder"].ID != id {
		t.Fatalf("lost partial destroy progress: %+v", f)
	}
	c.remote.FailOn = nil
	c.remote.Calls = nil
	if out, err := runCLI(t, "destroy", dir); err != nil {
		t.Fatalf("resume destroy: %v\n%s", err, out)
	}
	if strings.Join(c.remote.Calls, ",") != "delete "+id {
		t.Fatalf("repeated completed deletion: %v", c.remote.Calls)
	}
}

func TestStateV1RequiresExplicitExternalDeclaration(t *testing.T) {
	dir, c := migrationModule(t)
	downgradeState(t, dir)
	path := filepath.Join(dir, "kastor.hcl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.ReplaceAll(string(data), "  plugin = \"anthropic\"\n", ""))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// A fake legacy factory keeps this test independent of credentials.
	previous := providerFactories["claude_agents"]
	providerFactories["claude_agents"] = func(*schema.Target) (provider.Provider, error) { return c.remote, nil }
	t.Cleanup(func() { providerFactories["claude_agents"] = previous })
	before := readStateFile(t, dir)
	if out, err := runCLI(t, "apply", dir); err == nil || !strings.Contains(out, "v1 ownership is ambiguous") {
		t.Fatalf("error: %v\n%s", err, out)
	}
	if readStateFile(t, dir) != before || len(c.remote.Calls) != 0 {
		t.Fatal("ambiguous state changed")
	}
}

func TestStateV1MemoryIsExplicitlyBuiltin(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kastor.hcl"), []byte("target \"memory\" { type = \"platform\" }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const legacy = `{"version":1,"serial":4,"targets":{"memory":{"resources":{"agent.old":{"id":"mem-1","config":{}}}}}}`
	if err := os.WriteFile(filepath.Join(dir, state.Filename), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	jobs, release, err := preparePlatform(context.Background(), os.Stderr, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	owner := jobs[0].job.State.Targets["memory"].Plugin
	if owner.Source != state.MemorySource || owner.Version != "builtin" || owner.Protocol != 0 {
		t.Fatalf("memory owner: %+v", owner)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if readStateFile(t, dir) != legacy {
		t.Fatal("preparation wrote memory migration")
	}
	if out, err := runCLI(t, "destroy", dir); err != nil {
		t.Fatalf("memory destroy: %v\n%s", err, out)
	}
	f, err := state.Load(dir)
	if err != nil || f.Version != 2 || len(f.Targets) != 0 {
		t.Fatalf("memory destruction: %+v, %v", f, err)
	}
}

func TestStateTargetedMigrationResolvesWholeSnapshot(t *testing.T) {
	dir, c := migrationModule(t)
	path := filepath.Join(dir, "kastor.hcl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("\ntarget \"staging\" {\n type = \"platform\"\n plugin = \"anthropic\"\n}\n")
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("append target: %v, %v", err, closeErr)
	}
	rewriteState(t, dir, func(f *state.File) {
		f.Version = 1
		f.Targets["claude_agents"].Plugin = nil
		f.Targets["staging"] = &state.TargetState{Resources: map[string]*state.Resource{
			"agent.old": {ID: "staging-id", Config: json.RawMessage(`{"old":true}`)},
		}}
	})
	if out, err := runCLI(t, "apply", "--target", "claude_agents", dir); err != nil {
		t.Fatalf("targeted migration: %v\n%s", err, out)
	}
	st, err := state.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Targets["staging"].Plugin.Source != claudePluginSource || st.Targets["staging"].Resources["agent.old"].ID != "staging-id" {
		t.Fatal("unselected target not migrated intact")
	}
	assertNoRemoteMutations(t, c)
	// A subsequent targeted operation must not silently overlook a source
	// mismatch on another target in the snapshot it would write.
	rewriteState(t, dir, func(f *state.File) { f.Targets["staging"].Plugin.Source = "github.com/example/other" })
	c.remote.Calls = nil
	if out, err := runCLI(t, "apply", "--target", "claude_agents", dir); err == nil || !strings.Contains(out, "target.staging") {
		t.Fatalf("unselected mismatch: %v\n%s", err, out)
	}
	if len(c.remote.Calls) != 0 {
		t.Fatal("mismatch reached remote")
	}
}
