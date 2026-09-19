package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/getkastordev/kastor/internal/module"
	pluginruntime "github.com/getkastordev/kastor/internal/plugin"
	"github.com/getkastordev/kastor/internal/provider"
	"github.com/getkastordev/kastor/internal/schema"
	"github.com/getkastordev/kastor/internal/state"
)

// bindStateOwnership checks every managed target before any lifecycle calls.
// An unselected v2 target retains its last resolved version; v1 needs verified
// metadata for all managed targets because writes replace the whole snapshot.
func bindStateOwnership(ctx context.Context, mod *module.Module, st *state.File, jobs []*platformJob) (bool, error) {
	selected := map[string]*platformJob{}
	owners := map[string]state.PluginIdentity{}
	for _, pj := range jobs {
		selected[pj.job.Target.Name] = pj
		owners[pj.job.Target.Name] = resolvedIdentity(pj)
	}
	targets := map[string]*schema.Target{}
	for _, tgt := range mod.Targets {
		targets[tgt.Name] = tgt
	}
	names := make([]string, 0, len(st.Targets))
	for name := range st.Targets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ts := st.Targets[name]
		if len(ts.Resources) == 0 {
			continue
		}
		tgt := targets[name]
		if tgt == nil || tgt.Type != "platform" {
			return false, fmt.Errorf("%s: target.%s: managed target has no platform declaration; restore its original target and plugin declaration before proceeding", state.Filename, name)
		}
		source, err := declaredStateSource(mod, tgt)
		if err != nil {
			return false, err
		}
		if st.Version == 1 {
			if tgt.Plugin == "" && name != "memory" {
				return false, fmt.Errorf("%s: %s: v1 ownership is ambiguous; declare an explicit plugin in required_plugins and on the target before migration", state.Filename, tgt.Addr())
			}
			// Reserve legacy labels conservatively: v1 cannot prove whether
			// their old resources came from an explicitly selected replacement.
			if (name == "memory" && source != state.MemorySource) || (name == "claude_agents" && source != claudePluginSource) {
				return false, fmt.Errorf("%s: %s: plugin source %q does not match the v1 legacy owner; restore the original implementation before migration", state.Filename, tgt.Addr(), source)
			}
		} else {
			if ts.Plugin.Source != source {
				return false, fmt.Errorf("%s: %s: plugin source %q does not match managed owner %q; restore the original declaration or destroy with the original owner before switching", state.Filename, tgt.Addr(), source, ts.Plugin.Source)
			}
			if selected[name] == nil {
				owners[name] = *ts.Plugin
				continue
			}
		}
		if selected[name] == nil {
			p, closeProvider, err := providerFor(ctx, mod, tgt)
			if err != nil {
				return false, err
			}
			owners[name] = resolvedIdentity(&platformJob{job: &provider.Job{Module: mod, Target: tgt}, provider: p})
			if closeProvider != nil {
				if err := closeProvider(); err != nil {
					return false, fmt.Errorf("%s: close migration plugin: %w", tgt.Addr(), err)
				}
			}
		}
	}
	changed := st.Version == 1
	for name, owner := range owners {
		if ts := st.Targets[name]; ts != nil && len(ts.Resources) > 0 && (ts.Plugin == nil || *ts.Plugin != owner) {
			changed = true
		}
	}
	return changed, st.Bind(owners)
}

func declaredStateSource(mod *module.Module, tgt *schema.Target) (string, error) {
	if tgt.Plugin != "" {
		return targetPluginSource(mod, tgt)
	}
	if tgt.Name == "memory" {
		return state.MemorySource, nil
	}
	if tgt.Name == "claude_agents" {
		return claudePluginSource, nil
	}
	return "builtin/" + tgt.Name, nil
}

func resolvedIdentity(pj *platformJob) state.PluginIdentity {
	if external, ok := pj.provider.(*pluginruntime.Platform); ok {
		m := external.Client.Metadata()
		return state.PluginIdentity{Source: m.Source, Version: m.Version, Protocol: m.Protocol}
	}
	source, _ := declaredStateSource(pj.job.Module, pj.job.Target)
	return state.PluginIdentity{Source: source, Version: "builtin", Protocol: 0}
}
