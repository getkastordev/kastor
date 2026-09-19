# Local state v2 migration (KAS-78)

The September 21, 2026 release preparation introduces state format v2. Kastor
reads v1 and v2, writes v2, and rejects unsupported formats, including future
versions. This is a local-file migration; it adds no remote backend.

## What changes

Each managed platform target records its implementation:

```json
"plugin": {
  "source": "github.com/getkastordev/kastor-anthropic",
  "version": "0.1.0",
  "protocol": 1
}
```

The source is ownership. Version and protocol are resolved metadata, not a
version constraint or part of the target label. They come from the validated
plugin handshake: normally the lock-selected artifact, or the actual binary
when using a development override or the no-lock PATH fallback. A plugin alias
rename or version upgrade for the same source is allowed if resolution accepts
its version and protocol. This does not guarantee that a plugin release has
backward-compatible mappings; review the plan for actual configuration changes.

Target labels, resource IDs, dependencies, and last-applied configuration are
preserved during migration. Equivalent normalized configs written by the legacy
Claude adapter remain intact. A format or version-metadata change alone causes
no remote create, update, or delete operations.

## Before the first apply or destroy

1. Stop concurrent Kastor commands. Save a separate copy of `kastor.state.json`,
   your module files, and `.kastor.lock.hcl` if present. For example, from the
   module root:

   ```sh
   cp kastor.state.json kastor.state.json.pre-v2-backup
   cp .kastor.lock.hcl .kastor.lock.hcl.pre-v2-backup
   ```

   Skip the second command if the module has no lock. Kastor does **not** create
   automatic backups. Keep environment-specific state and backups out of Git.

2. Keep every managed target's **original label** and declare its original
   plugin explicitly. For a v0.2 Claude target:

   ```hcl
   kastor {
     required_plugins {
       anthropic = {
         source  = "github.com/getkastordev/kastor-anthropic"
         version = "~> 0.1"
       }
     }
   }

   target "claude_agents" {
     type   = "platform"
     plugin = "anthropic"
   }
   ```

   Retain the target's existing `config` block. Resolve/install the plugin with
   `kastor init` as usual. Migration never infers identity from config contents
   or remote IDs. A missing target, codegen replacement, or missing explicit
   selector for a managed external v1 target is rejected. The legacy
   `claude_agents` label must select the official Anthropic source above.

   For other v1 target labels, the explicit declaration is your assertion of
   the original owner: v1 has no recorded source with which to verify it. Use
   the original module/lock history, not a guessed replacement plugin.

3. Run `kastor plan`. Review any drift or pending changes. Run `kastor doctor`
   separately for readiness if needed. Neither command writes migration or
   version metadata, even when checks succeed.

4. Run `kastor apply` when ready to perform the reviewed changes. A successful
   no-op apply is enough to persist a metadata-only migration. A changed source
   is rejected while resources remain managed; restore the original source to
   proceed. There is no automatic source-rebinding or remote-object transfer.

## Exact persistence behavior

| Command/result | State behavior |
| --- | --- |
| Plan or doctor | Resolve/check ownership in memory; leave file and serial unchanged |
| Apply: successful create/update/delete or stale-config refresh | Atomically save v2 immediately, increment serial once per write |
| Apply: success with only migration/version metadata pending | Save v2 once after the successful no-op apply; no remote mutation |
| Apply: unchanged v2 config and metadata | No write |
| Apply/destroy: first operation fails without a successful state mutation | Original state remains unchanged |
| Apply/destroy: later operation fails | Successful prefix is already saved as v2; retry plans the remainder |
| Destroy: successful deletion | Save v2 immediately; remove empty targets and their ownership |
| Destroy: nothing managed | No write |
| Unsupported state format or source mismatch | Error; no lifecycle calls or state writes |

Ownership validation covers **all managed targets**, including with `--target`.
Every managed v1 target needs resolvable metadata before any part of the file
can be written as v2. For unselected v2 targets, the stored resolved version is
retained and the declared source is checked. Removing a target declaration does
not relinquish management; restore it to reconcile or destroy its resources.

All state commands retain local locking with `.kastor.state.lock`. State writes
still use a temporary file and atomic rename. Failed writes do not advance the
in-memory serial. Metadata is committed in the same snapshot as resource
progress, including successful operations before a later failure.

### Built-in memory and legacy adapters

The selector-free `target "memory"` migrates without `required_plugins`:
its source is `builtin/memory`, version is `builtin`, and protocol is `0` because
there is no executable handshake. Switching existing v1 memory resources to an
external plugin is rejected. Memory objects die with the process, so a later
invocation may plan creates for genuinely missing objects; the migration does
not suppress that drift.

New state produced by in-process compatibility adapters uses version `builtin`
and protocol `0`. The legacy Claude adapter uses the official Anthropic source,
allowing its v2 state to move to the executable of the same source. Existing
non-memory **v1** state still requires an explicit plugin declaration to migrate.

## Recovery and rollback

- **Ambiguous ownership or mismatch:** restore the original target label and
  source declaration. Do not edit `plugin.source` to bypass the guard. To switch
  sources, first destroy with the original owner; after all managed resources
  are gone, declare the new source. On Claude, destroy archives irreversibly.
- **Partial apply/destroy:** keep the newest state. Fix the reported failure and
  retry with the same owner. Restoring the pre-v2 backup now would discard
  successful progress and can orphan or recreate remote objects.
- **State write failure after a remote operation:** retain the diagnostic and
  any reported remote ID. Back up the current state and temporary file, fix the
  filesystem problem, and reconcile the completed remote operation into a
  verified recovery snapshot before retrying. There is no automatic import or
  remote rollback. In particular, do not blindly retry a create whose ID was
  never saved.
- **Stale lock:** only remove `.kastor.state.lock` after confirming the named
  process has stopped and no other Kastor command is running.
- **Binary rollback:** older v1-only Kastor binaries reject v2. Restore the v1
  backup and matching module/lock only if no remote changes have occurred since
  that backup (for example, after a metadata-only no-op migration). Otherwise
  keep v2 and use a v2-capable binary. Changing the JSON `version` alone is not a
  supported downgrade.
