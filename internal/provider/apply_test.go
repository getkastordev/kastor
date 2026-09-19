package provider_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/getkastordev/kastor/internal/provider"
	"github.com/getkastordev/kastor/internal/provider/providertest"
	"github.com/getkastordev/kastor/internal/state"
)

// countingSave returns a save callback that counts invocations.
func countingSave(n *int) func() error {
	return func() error {
		*n++
		return nil
	}
}

// buildPlan plans against the fake and resets its call log, so the call
// assertions in this file cover the apply under test and not the read/diff
// calls the planning phase made first.
func buildPlan(t *testing.T, fake *providertest.Fake, job *provider.Job) *provider.Plan {
	t.Helper()
	plan, err := provider.BuildPlan(context.Background(), fake, job)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	fake.Calls = nil
	return plan
}

func TestApplyFreshModuleCreatesEverything(t *testing.T) {
	job := newJob(t)
	fake := providertest.New()
	plan := buildPlan(t, fake, job)

	var saves int
	applied, err := provider.Apply(context.Background(), fake, job, plan, countingSave(&saves))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied != 3 {
		t.Errorf("applied = %d, want 3", applied)
	}
	if saves != 3 {
		t.Errorf("state saved %d times, want once per change", saves)
	}

	wantCalls := []string{"create agent.geocoder", "create agent.forecast", "create agent.weather"}
	if diff := cmp.Diff(wantCalls, fake.Calls); diff != "" {
		t.Errorf("provider calls (-want +got):\n%s", diff)
	}

	ts := job.State.Target("fake")
	wantState := map[string]struct {
		id   string
		deps []string
	}{
		"agent.geocoder": {id: "fake-1"},
		"agent.forecast": {id: "fake-2", deps: []string{"agent.geocoder"}},
		"agent.weather":  {id: "fake-3", deps: []string{"agent.forecast", "agent.geocoder"}},
	}
	for addr, want := range wantState {
		res, ok := ts.Resources[addr]
		if !ok {
			t.Errorf("state has no %s", addr)
			continue
		}
		if res.ID != want.id {
			t.Errorf("%s: ID = %q, want %q", addr, res.ID, want.id)
		}
		if diff := cmp.Diff(want.deps, res.Dependencies); diff != "" {
			t.Errorf("%s: dependencies (-want +got):\n%s", addr, diff)
		}
		if len(res.Config) == 0 {
			t.Errorf("%s: no last-applied config recorded", addr)
		}
	}

	// The follow-up plan must be all noops.
	fake.Calls = nil
	replan := buildPlan(t, fake, job)
	if c, u, d, n := replan.Counts(); c != 0 || u != 0 || d != 0 || n != 3 {
		t.Errorf("replan counts = %d,%d,%d,%d; want 0,0,0,3", c, u, d, n)
	}
}

func TestApplyStoresProviderNormalizedConfig(t *testing.T) {
	job := newJob(t)
	p := &stateNormalizingProvider{Fake: providertest.New()}
	plan, err := provider.BuildPlan(context.Background(), p, job)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	if _, err := provider.Apply(context.Background(), p, job, plan, countingSave(new(int))); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for addr, resource := range job.State.Target("fake").Resources {
		var config provider.Object
		if err := json.Unmarshal(resource.Config, &config); err != nil {
			t.Fatalf("%s: decode state config: %v", addr, err)
		}
		if config["normalized_for_state"] != addr {
			t.Errorf("%s: state config = %#v, want provider-normalized marker", addr, config)
		}
		if _, exists := config["model"]; exists {
			t.Errorf("%s: state retained raw desired config: %#v", addr, config)
		}
	}
}

func TestApplyNoopRefreshStoresProviderNormalizedConfig(t *testing.T) {
	job := newJob(t)
	p := &stateNormalizingProvider{Fake: providertest.New()}
	job.State.Target("fake").Resources["agent.geocoder"] = &state.Resource{
		ID:     "fake-1",
		Config: json.RawMessage(`{"raw":true}`),
	}
	plan := &provider.Plan{
		Target: "fake",
		Changes: []provider.Change{{
			Addr:   "agent.geocoder",
			Action: provider.ActionNoop,
			ID:     "fake-1",
			Drift:  []provider.AttrDiff{{Path: "raw", Old: nil, New: true}},
		}},
	}

	var saves int
	if _, err := provider.Apply(context.Background(), p, job, plan, countingSave(&saves)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if saves != 1 {
		t.Fatalf("state saves = %d, want 1", saves)
	}
	var config provider.Object
	raw := job.State.Target("fake").Resources["agent.geocoder"].Config
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatalf("decode state config: %v", err)
	}
	if config["normalized_for_state"] != "agent.geocoder" {
		t.Errorf("state config = %#v, want provider-normalized marker", config)
	}
}

type stateNormalizingProvider struct {
	*providertest.Fake
}

func (*stateNormalizingProvider) NormalizeStateConfig(desired *provider.Resource) (provider.Object, error) {
	return provider.Object{"normalized_for_state": desired.Addr}, nil
}

func TestApplyPartialFailureThenRecovery(t *testing.T) {
	job := newJob(t)
	fake := providertest.New()
	boom := errors.New("quota exceeded")
	fake.FailOn = map[string]error{"create agent.forecast": boom}

	var saves int
	applied, err := provider.Apply(context.Background(), fake, job, buildPlan(t, fake, job), countingSave(&saves))
	if err == nil {
		t.Fatal("Apply succeeded, want error")
	}
	if !errors.Is(err, boom) {
		t.Errorf("error %v does not wrap the provider error", err)
	}
	var applyErr *provider.ApplyError
	if !errors.As(err, &applyErr) {
		t.Fatalf("error %T is not an *ApplyError", err)
	}
	if applyErr.Addr != "agent.forecast" || applyErr.Action != provider.ActionCreate || applyErr.Applied != 1 {
		t.Errorf("ApplyError = %+v; want addr agent.forecast, action create, 1 applied", applyErr)
	}
	if applied != 1 || saves != 1 {
		t.Errorf("applied = %d, saves = %d; want 1, 1", applied, saves)
	}
	// The message tells the user where it stopped and what happened.
	for _, want := range []string{"agent.forecast", "create", "1 of 3", "quota exceeded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}

	// What landed before the failure is in state...
	ts := job.State.Target("fake")
	if _, ok := ts.Resources["agent.geocoder"]; !ok {
		t.Error("agent.geocoder missing from state after partial apply")
	}
	if _, ok := ts.Resources["agent.forecast"]; ok {
		t.Error("failed agent.forecast landed in state")
	}

	// ...and a re-run picks up exactly the remaining work.
	fake.FailOn = nil
	fake.Calls = nil
	replan := buildPlan(t, fake, job)
	var wantReplan = []string{"noop agent.geocoder", "create agent.forecast", "create agent.weather"}
	if diff := cmp.Diff(wantReplan, changeSummary(replan)); diff != "" {
		t.Errorf("replan (-want +got):\n%s", diff)
	}
	applied, err = provider.Apply(context.Background(), fake, job, replan, countingSave(&saves))
	if err != nil {
		t.Fatalf("recovery Apply: %v", err)
	}
	if applied != 2 {
		t.Errorf("recovery applied = %d, want 2", applied)
	}
}

func TestApplySaveFailureNamesTheRemoteID(t *testing.T) {
	job := newJob(t)
	fake := providertest.New()
	diskFull := errors.New("disk full")

	_, err := provider.Apply(context.Background(), fake, job, buildPlan(t, fake, job), func() error { return diskFull })
	if err == nil {
		t.Fatal("Apply succeeded, want error")
	}
	if !errors.Is(err, diskFull) {
		t.Errorf("error %v does not wrap the save error", err)
	}
	// The remote object exists but state does not know: the error must
	// hand the user the id.
	for _, want := range []string{"agent.geocoder", "fake-1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestApplyUpdateConvergesRemoteAndState(t *testing.T) {
	job := newJob(t)
	fake := providertest.New()
	ids := seedAll(t, job, fake)

	// Remote drifted; the plan converges it back to the spec.
	fake.Objects[ids["agent.weather"]]["instructions"] = "hacked"

	var saves int
	applied, err := provider.Apply(context.Background(), fake, job, buildPlan(t, fake, job), countingSave(&saves))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied != 1 || saves != 1 {
		t.Errorf("applied = %d, saves = %d; want 1, 1", applied, saves)
	}
	remote, _, _ := fake.Read(context.Background(), ids["agent.weather"])
	if remote["instructions"] == "hacked" {
		t.Error("remote object was not converged back to the spec")
	}
}

func TestApplyDeleteRemovedResource(t *testing.T) {
	job := newJob(t)
	fake := providertest.New()
	seedAll(t, job, fake)

	ts := job.State.Target("fake")
	ts.Resources["agent.legacy"] = &state.Resource{ID: "fake-99", Config: json.RawMessage(`{}`)}
	fake.Objects["fake-99"] = provider.Object{}

	applied, err := provider.Apply(context.Background(), fake, job, buildPlan(t, fake, job), countingSave(new(int)))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1", applied)
	}
	if _, ok := ts.Resources["agent.legacy"]; ok {
		t.Error("deleted resource still tracked in state")
	}
	if _, ok := fake.Objects["fake-99"]; ok {
		t.Error("remote object still exists after delete")
	}
}

func TestApplyDeleteOfMissingRemoteStillCleansState(t *testing.T) {
	job := newJob(t)
	fake := providertest.New()
	seedAll(t, job, fake)

	// Tracked in state, gone from the platform, and gone from the spec:
	// the delete must still succeed and clean up state.
	ts := job.State.Target("fake")
	ts.Resources["agent.legacy"] = &state.Resource{ID: "fake-99", Config: json.RawMessage(`{}`)}

	applied, err := provider.Apply(context.Background(), fake, job, buildPlan(t, fake, job), countingSave(new(int)))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied != 1 {
		t.Errorf("applied = %d, want 1", applied)
	}
	if _, ok := ts.Resources["agent.legacy"]; ok {
		t.Error("resource with missing remote still tracked in state")
	}
}

func TestApplyNoopRefreshesStaleStateConfig(t *testing.T) {
	job := newJob(t)
	fake := providertest.New()
	seedAll(t, job, fake)

	// State recorded an older config, but remote already matches the spec
	// (user aligned the spec with a manual change). Apply must refresh the
	// state entry without touching the remote.
	ts := job.State.Target("fake")
	var cfg provider.Object
	if err := json.Unmarshal(ts.Resources["agent.geocoder"].Config, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["model"].(map[string]any)["id"] = "gpt-4o"
	staleRaw, _ := provider.MarshalConfig(cfg)
	ts.Resources["agent.geocoder"].Config = staleRaw

	plan := buildPlan(t, fake, job)
	fake.Calls = nil

	var saves int
	applied, err := provider.Apply(context.Background(), fake, job, plan, countingSave(&saves))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied != 0 {
		t.Errorf("applied = %d, want 0 (refresh is not a remote change)", applied)
	}
	if saves != 1 {
		t.Errorf("saves = %d, want 1 (the refreshed entry must be persisted)", saves)
	}
	if len(fake.Calls) != 0 {
		t.Errorf("noop refresh touched the provider: %v", fake.Calls)
	}
	if string(ts.Resources["agent.geocoder"].Config) == string(staleRaw) {
		t.Error("stale state config was not refreshed")
	}

	// Drift warning must not recur once state is refreshed.
	replan := buildPlan(t, fake, job)
	if len(replan.Diagnostics) != 0 {
		t.Errorf("drift warning recurred after refresh: %+v", replan.Diagnostics)
	}
}

func TestApplyDestroyEmptiesState(t *testing.T) {
	job := newJob(t)
	fake := providertest.New()
	seedAll(t, job, fake)
	fake.Calls = nil

	plan, err := provider.BuildDestroyPlan(job)
	if err != nil {
		t.Fatalf("BuildDestroyPlan: %v", err)
	}
	applied, err := provider.Apply(context.Background(), fake, job, plan, countingSave(new(int)))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied != 3 {
		t.Errorf("applied = %d, want 3", applied)
	}

	wantCalls := []string{"delete fake-3", "delete fake-2", "delete fake-1"}
	if diff := cmp.Diff(wantCalls, fake.Calls); diff != "" {
		t.Errorf("provider calls (-want +got):\n%s", diff)
	}
	if n := len(job.State.Target("fake").Resources); n != 0 {
		t.Errorf("state still tracks %d resources after destroy", n)
	}
	if n := len(fake.Objects); n != 0 {
		t.Errorf("%d remote objects remain after destroy", n)
	}
}
