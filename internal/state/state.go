// Package state owns the kastor.state.json file (SPEC.md §5): the record of
// which remote resource each block address maps to and the configuration
// last applied to it, used by kastor plan/apply for three-way comparison and
// drift detection.
//
// Determinism guarantee: writing equal logical state always produces
// byte-identical files — struct fields serialize in declared order, maps in
// sorted key order, and configs are stored as canonical JSON produced by
// the provider engine.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// Version is the current write format. Version 1 can be read, but must have
// ownership resolved from the module before it can be written as version 2.
const Version = 2

// Filename is the state file's name, fixed at the module root.
const Filename = "kastor.state.json"

// File is the decoded state file. Serial increases by one on every Write,
// so any two snapshots of the same module's state are ordered.
type File struct {
	Version int                     `json:"version"`
	Serial  uint64                  `json:"serial"`
	Targets map[string]*TargetState `json:"targets"`
}

// TargetState is the managed resource set of one platform target.
type TargetState struct {
	Plugin    *PluginIdentity      `json:"plugin,omitempty"`
	Resources map[string]*Resource `json:"resources"`
}

// PluginIdentity records the implementation, not the module-local plugin alias.
// Version and Protocol describe the resolved implementation; only Source is
// ownership. In-process implementations use version "builtin", protocol 0.
type PluginIdentity struct {
	Source   string `json:"source"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

// MemorySource is reserved for the ephemeral in-process memory platform.
const MemorySource = "builtin/memory"

func (p *PluginIdentity) valid() bool {
	if p == nil || p.Source == "" || p.Version == "" {
		return false
	}
	if p.Version == "builtin" {
		return p.Protocol == 0
	}
	return p.Protocol > 0
}

// Bind resolves ownership in memory only. Callers supply verified metadata and
// must resolve every managed v1 target, even for a targeted operation, because
// the next atomic snapshot covers the entire file. Validation is all-or-nothing.
func (f *File) Bind(owners map[string]PluginIdentity) error {
	if f.Version != 1 && f.Version != Version {
		return fmt.Errorf("%s: unsupported state version %d", Filename, f.Version)
	}
	names := make([]string, 0, len(f.Targets))
	for name := range f.Targets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		ts := f.Targets[name]
		if ts == nil {
			return fmt.Errorf("%s: target.%s: null target state; expected a resource set", Filename, name)
		}
		if len(ts.Resources) == 0 {
			continue
		}
		owner, ok := owners[name]
		if !ok || !owner.valid() {
			return fmt.Errorf("%s: target.%s: unresolved plugin ownership; restore the target and declare its explicit plugin before migration or reconciliation", Filename, name)
		}
		if f.Version == Version && !ts.Plugin.valid() {
			return fmt.Errorf("%s: target.%s: missing plugin identity in v2 state; restore a valid state backup", Filename, name)
		}
		if ts.Plugin != nil && ts.Plugin.Source != owner.Source {
			return fmt.Errorf("%s: target.%s: plugin source %q does not match managed owner %q; restore the original plugin declaration, or destroy with the original owner before switching", Filename, name, owner.Source, ts.Plugin.Source)
		}
	}
	for name, owner := range owners {
		if !owner.valid() {
			return fmt.Errorf("%s: target.%s: incomplete resolved plugin identity", Filename, name)
		}
	}
	for name, owner := range owners {
		f.Target(name).Plugin = &owner
	}
	return nil
}

// Resource records one managed remote resource: the remote ID it maps to,
// the canonical JSON of the configuration last applied to it (full config,
// not a hash — drift reports name the attributes that changed), and its
// managed dependencies (block addresses), kept so a resource removed from
// the spec can still be destroyed in reverse dependency order.
type Resource struct {
	ID           string          `json:"id"`
	Config       json.RawMessage `json:"config"`
	Dependencies []string        `json:"dependencies,omitempty"`
}

// Load reads the state file at the root of dir. A missing file is an empty
// state, not an error — every module starts unmanaged.
func Load(dir string) (*File, error) {
	path := filepath.Join(dir, Filename)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &File{Version: Version, Targets: map[string]*TargetState{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state file: %w", err)
	}

	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: parsing state file: %w", path, err)
	}
	if f.Version != 1 && f.Version != Version {
		return nil, fmt.Errorf("%s: state file version %d is not supported by this kastor (supports versions 1 and %d)", path, f.Version, Version)
	}
	if f.Targets == nil {
		f.Targets = map[string]*TargetState{}
	}
	for name, ts := range f.Targets {
		if ts == nil || (f.Version == Version && len(ts.Resources) > 0 && !ts.Plugin.valid()) {
			return nil, fmt.Errorf("%s: target.%s: invalid target ownership; expected a resource set with plugin identity in v2", path, name)
		}
	}
	return &f, nil
}

// Write bumps the serial and atomically replaces the state file at the root
// of dir (temp file + rename, so a crash never leaves a torn file). Targets
// with no resources are dropped from the output — an empty entry carries no
// information and would accumulate forever.
func (f *File) Write(dir string) error {
	if f.Version != 1 && f.Version != Version {
		return fmt.Errorf("%s: unsupported state version %d", Filename, f.Version)
	}
	out := &File{Version: Version, Serial: f.Serial + 1, Targets: map[string]*TargetState{}}
	for name, ts := range f.Targets {
		if ts == nil {
			return fmt.Errorf("%s: target.%s: null target state", Filename, name)
		}
		if len(ts.Resources) > 0 {
			if !ts.Plugin.valid() {
				return fmt.Errorf("%s: target.%s: unresolved plugin ownership; cannot write v2 state", Filename, name)
			}
			out.Targets[name] = ts
		}
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding state file: %w", err)
	}
	data = append(data, '\n')

	path := filepath.Join(dir, Filename)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing state file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("writing state file: %w", err)
	}
	f.Version = out.Version
	f.Serial = out.Serial
	return nil
}

// Target returns the state of one platform target, creating an empty entry
// if the target is not tracked yet.
func (f *File) Target(name string) *TargetState {
	if ts, ok := f.Targets[name]; ok {
		if ts.Resources == nil {
			ts.Resources = map[string]*Resource{}
		}
		return ts
	}
	ts := &TargetState{Resources: map[string]*Resource{}}
	f.Targets[name] = ts
	return ts
}
