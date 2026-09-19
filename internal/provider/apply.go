package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/getkastordev/kastor/internal/state"
)

// ApplyError reports the change that stopped an apply. Everything before
// it was applied and saved to state — a re-run plans only the remainder.
type ApplyError struct {
	Addr    string // block address of the failed change
	Action  Action
	Applied int // changes completed before the failure
	Total   int // changes the plan wanted (noops excluded)
	Err     error
}

func (e *ApplyError) Error() string {
	return fmt.Sprintf("%s: %s failed (%d of %d changes applied, state saved): %v",
		e.Addr, e.Action, e.Applied, e.Total, e.Err)
}

func (e *ApplyError) Unwrap() error { return e.Err }

// Apply executes a plan's changes in order, persisting state via save
// after every completed operation, so a failure or crash never loses the
// mapping of what already exists remotely. The first failure aborts with
// an *ApplyError; earlier changes stay applied and saved.
//
// Noops are free with one exception: when the state's recorded config is
// stale even though the remote matches the spec (the user aligned the spec
// with a manual remote change), the state entry is refreshed — without any
// remote call — so the drift warning does not recur forever.
func Apply(ctx context.Context, p Provider, job *Job, plan *Plan, save func() error) (applied int, err error) {
	_, _, _, noops := plan.Counts()
	total := len(plan.Changes) - noops
	ts := job.State.Target(job.Target.Name)

	fail := func(c Change, err error) (int, error) {
		return applied, &ApplyError{Addr: c.Addr, Action: c.Action, Applied: applied, Total: total, Err: err}
	}

	for _, c := range plan.Changes {
		switch c.Action {
		case ActionCreate:
			desired, err := desiredResource(job, c.Addr)
			if err != nil {
				return fail(c, err)
			}
			id, err := p.Create(ctx, desired)
			if err != nil {
				return fail(c, err)
			}
			res, err := stateEntry(p, job, id, desired)
			if err != nil {
				return fail(c, err)
			}
			ts.Resources[c.Addr] = res
			if err := save(); err != nil {
				return fail(c, fmt.Errorf("created remotely as %s, but saving state failed — record the id manually or re-run after fixing: %w", id, err))
			}
			applied++

		case ActionUpdate:
			desired, err := desiredResource(job, c.Addr)
			if err != nil {
				return fail(c, err)
			}
			if err := p.Update(ctx, c.ID, desired); err != nil {
				return fail(c, err)
			}
			res, err := stateEntry(p, job, c.ID, desired)
			if err != nil {
				return fail(c, err)
			}
			ts.Resources[c.Addr] = res
			if err := save(); err != nil {
				return fail(c, fmt.Errorf("updated remotely, but saving state failed: %w", err))
			}
			applied++

		case ActionDelete:
			if err := p.Delete(ctx, c.ID); err != nil {
				return fail(c, err)
			}
			delete(ts.Resources, c.Addr)
			if err := save(); err != nil {
				return fail(c, fmt.Errorf("deleted remotely, but saving state failed: %w", err))
			}
			applied++

		case ActionNoop:
			// The plan already compared both desired and last-applied config
			// with the real remote using the provider's Diff. If both match,
			// retain the recorded config, even if a legacy adapter wrote a
			// different normalized representation. A format migration alone
			// must not replace last-applied configuration.
			if len(c.Drift) == 0 {
				continue
			}
			refreshed, err := refreshStale(p, job, ts, c.Addr)
			if err != nil {
				return fail(c, err)
			}
			if refreshed {
				if err := save(); err != nil {
					return fail(c, fmt.Errorf("refreshing state failed: %w", err))
				}
			}
		}
	}
	return applied, nil
}

// stateConfigNormalizer is an optional provider capability. Providers whose
// remote comparison shape differs from the neutral module config use it to
// persist the resolved last-applied form without changing the Provider
// contract or the state schema.
type stateConfigNormalizer interface {
	NormalizeStateConfig(desired *Resource) (Object, error)
}

// stateEntry builds the state record for a just-applied resource: remote
// id, canonical config, and its managed dependencies from the graph.
func stateEntry(p Provider, job *Job, id string, desired *Resource) (*state.Resource, error) {
	config, err := stateConfig(p, desired)
	if err != nil {
		return nil, err
	}
	raw, err := MarshalConfig(config)
	if err != nil {
		return nil, err
	}
	var deps []string
	for _, dep := range job.Graph.Dependencies(desired.Addr) {
		if strings.HasPrefix(dep, "agent.") {
			deps = append(deps, dep)
		}
	}
	return &state.Resource{ID: id, Config: raw, Dependencies: deps}, nil
}

func stateConfig(p Provider, desired *Resource) (Object, error) {
	if normalizer, ok := p.(stateConfigNormalizer); ok {
		return normalizer.NormalizeStateConfig(desired)
	}
	return desired.Config, nil
}

// refreshStale rewrites a noop resource's state entry when its recorded
// config no longer matches the spec, reporting whether it did.
func refreshStale(p Provider, job *Job, ts *state.TargetState, addr string) (bool, error) {
	res, ok := ts.Resources[addr]
	if !ok {
		return false, nil
	}
	desired, err := desiredResource(job, addr)
	if err != nil {
		return false, err
	}
	stale, err := configStale(p, desired, res.Config)
	if err != nil || !stale {
		return false, err
	}
	fresh, err := stateEntry(p, job, res.ID, desired)
	if err != nil {
		return false, err
	}
	ts.Resources[addr] = fresh
	return true, nil
}
