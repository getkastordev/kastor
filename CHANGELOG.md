# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Kastor is pre-1.0: the v0 language semantics may still change until the
v0 exit criteria KAS-36 are met.

## [Unreleased]

### Changed

- Local state now writes format v2 with per-target canonical plugin ownership
  and resolved version/protocol metadata (KAS-78). V1 migrates from explicit
  declarations, with a built-in memory exception. Plan and doctor never persist
  migrations; successful apply/destroy state mutations do, including a no-op
  apply that only records metadata. Source changes with managed resources and
  ambiguous migrations are rejected. Back up before migrating; v1-only binaries
  cannot read v2. See [STATE_MIGRATION.md](STATE_MIGRATION.md).

- Prepared the canonical core repository and Go module migration from
  `weirdGuy/kastor` to `getkastordev/kastor`. Go consumers must change imports
  and select a new-path core release; a GitHub redirect does not rewrite old
  module declarations. Plugin source identities and protocol v1 are unchanged.
  The Homebrew tap also moved to `getkastordev/homebrew-tap`, with install
  command `brew install getkastordev/tap/kastor`. See [RELEASING.md](RELEASING.md)
  for publication prerequisites and installation verification.

### Added

- Explicit target plugin requirements and selectors (KAS-76)

  Project files can declare `kastor.required_plugins` entries with stable
  source and version coordinates, and each target can select one with
  `plugin`. Target labels are now instance identity rather than implementation
  selection. Target-specific settings live in an opaque `config` block owned
  by the selected plugin; core parsing and module validation no longer contain
  LangGraph, eve, or Claude capability matrices. Existing v0.2 label-based
  targets remain temporary CLI aliases, while legacy target `auth` and
  top-level `vault_id` receive migration errors.

- `requires_approval` on agent blocks (KAS-66)

  `tools` remains the grant; `requires_approval = [tool.x]` narrows granted
  tools by pausing their calls for a human. Every entry must also appear in
  `tools`, duplicates and non-tool/unknown references are errors, and omission
  from `tools` remains the denial. LangGraph emits
  `HumanInTheLoopMiddleware` with an `InMemorySaver` checkpointer, eve emits
  authored-tool or per-MCP-tool approval gates, and Claude Managed Agents maps
  the subset to `always_ask` while keeping the rest `always_allow`. The policy
  stays in normalized state so console changes are drift, and `kastor doctor`
  verifies the remote gate split exactly.

- `kastor doctor`: a read-only readiness check answering the question `plan`
  cannot (KAS-63)

  `plan` and `apply` are about declared configuration — "does the remote match
  the spec". They cannot tell you whether what is deployed can actually serve a
  request: an agent whose MCP connections are unauthenticated and whose tool
  permissions deny everything matches its spec exactly, plans clean, and answers
  nothing. `kastor doctor [--target name] [dir]` checks, for every agent in
  state, that the remote object exists, that each `connection://` credential
  resolves in the target's vault and points at the server that declares it, that
  the deployed agent is permitted to call the tools it declares, and which
  `env://` refs the module needs and are unset. It never invokes an agent, never
  changes a remote object, and never writes state. Exit 0 ready, 1 findings,
  2 usage/IO.

  Every check reports one of three outcomes, not two: `ok`, `failed`, or
  `unknown`. **"Could not verify" is never rendered as "missing"** — an
  unreachable vault and an absent credential are different facts and a user acts
  differently on each. `unknown` counts as a finding, and is counted separately
  in the summary. Credential ids print with their display name alongside —
  `cred_011CZ… ("HubSpot Prod")` — because the id has to be the identifier (a
  display name is nullable and non-unique on the platform) but output should
  still be readable.

- `mcp_server` blocks, credential references, and `config.vault_id` (KAS-63)

  `mcp://<server>/<tool>` now resolves against a declared `mcp_server` block, so
  an unknown server is a compile error instead of a run-time failure. Servers
  carry `transport` (`http` | `stdio`), an address, and optional `auth` blocks
  whose `ref` names *where* a credential lives (`env://NAME` or
  `connection://<credential_id>`) — never its value. `auth` blocks may be bound
  per target, which is what lets one server be authenticated on both the codegen
  and the platform path. The Anthropic plugin's target config gains `vault_id`,
  read only by `doctor`.

- The `langgraph` target generates `mcp_servers.json` from the module's
  `mcp_server` blocks (KAS-65)

  Connection config stops being a file you write and becomes a file the build
  writes — deterministic and marked do-not-edit, like everything else in the
  output directory. It holds connection config only: **a credential value never
  appears in it, and neither does an `auth.ref`.** Authentication is injected by
  the generated bridge, which maps each server to the environment variable its
  `env://` ref names and reads that variable in your own process at call time.
  `KASTOR_MCP_CONFIG` survives as a local override for one run — a development
  escape hatch for aiming at a local server instance — and `ensure_config()` now
  checks only that path, since the default one is generated.

- The `eve` target dials the url an `mcp_server` block declares (KAS-65)

  `connections/<server>.ts` carries the endpoint literally instead of reading
  `KASTOR_MCP_<SERVER>_URL`, and builds an `Authorization: Bearer` header in the
  existing headers callback from the server's `env://` ref. The credential read
  stays inside the callback, never at module top level, because `eve build`
  evaluates connection modules and a build has to succeed without deployment
  credentials. `stdio` transport and `connection://` refs are errors on this
  target — an eve connection is an HTTP client, and a generated project holds no
  platform connections.

### Changed

- `claude_agents` takes an MCP server's URL from its `mcp_server` block instead
  of from `KASTOR_MCP_<SERVER>_URL` (KAS-63)

  Reading the address from the environment made a target's desired configuration
  depend on the operator's shell, wrote a shell-derived value into the state
  file, and left drift on that attribute comparing one shell against another. A
  server's identity, address, and the location of its credential are spec now; a
  credential's value never is. Editing a server's `url` is an ordinary visible
  diff. **This is a breaking change for modules using MCP tools on
  `claude_agents`:** declare an `mcp_server` block with the URL the variable used
  to supply. The first `plan` after upgrading may show an `mcp_servers` diff if
  the two disagree.

- `kastor plan` no longer contacts the credential vault (KAS-63)

  An earlier design verified `connection://` credentials during `plan`, by way of
  a provider-contract amendment permitting `Diff` to issue reads. That is
  reverted: `Diff`'s entire output vocabulary is drift, the verification could
  never be reported as drift, and a check that cannot produce drift does not
  belong in the drift function. Verification moved to `kastor doctor`, and `plan`
  is a pure read again — it works offline, against the in-memory provider, and in
  network-restricted CI with no flag to disable anything.

- **Breaking:** `KASTOR_MCP_<SERVER>_URL` is gone from every target (KAS-65)

  KAS-63 removed it from `claude_agents`; it is now removed from `langgraph` and
  `eve` as well, which completes the move of a server's address out of the
  operator's shell and into the spec. There is no deprecation window and no
  fallback: pre-1.0, a fallback would keep the state-file defect it was removed
  for alive for another release. **A module that validates today and names an
  undeclared MCP server now fails**, with an error that names the fix —
  `declare mcp_server "<name>" in the project file`. Add one block per server,
  with the URL the variable used to supply.

- `kastor init` no longer scaffolds `mcp_servers.json` (KAS-65)

  The scaffold declares an `mcp_server "fetch"` block in `kastor.hcl` instead,
  and the build generates the connection config. Five scaffold files now rather
  than six, and one less thing to keep in sync by hand.

### Fixed

- `claude_agents`: agents applied to the Claude Managed Agents platform came up
  with every MCP tool denied, so `apply` reported success, `plan` reported no
  drift, and the deployed agent could not call a tool it declared (KAS-57)

  The provider created the agent without stating a per-tool permission, and the
  platform's own default for an unset permission is the restrictive one.
  Declaring a tool in the spec is now the grant: `create` sets
  `permission_policy` to `always_allow` on every tool in the agent closure, the
  permission is part of the compared object — so a tool flipped to deny in the
  Console is reported as drift — and `apply` reconciles it back to the spec.
  KAS-66 now makes that policy configurable through `requires_approval`; tools
  outside that subset keep this explicit allow default.

  The first plan after upgrading an agent applied by an earlier version reports
  the remote denial as drift and proposes the converging update.

### Added

- VS Code extension (`extensions/vscode/`): syntax highlighting and file icons
  for `.agent`, `.tool`, `.prompt`, `.kastor`, and `kastor.hcl` (KAS-56)

  Highlighting only — no language server, no commands, no settings. The grammar
  uses HashiCorp's TextMate scope vocabulary, so Kastor inherits each theme's
  Terraform colors, and adds rules for the constructs that are Kastor's own:
  block references, the bare type keywords, the `source` kind and `target` type
  enums, and `{{variable}}` prompt templates. HCL that Kastor rejects
  (`${...}`, heredocs, functions, `for`, ternaries) is deliberately left
  uncolored. Icons ship as language icons and appear under the default Seti
  icon theme with no configuration. Packaged but not yet published; the
  extension's README documents the publish command.

## [0.2.0] - 2026-07-30

### Added

- Claude Managed Agents platform provider: `target "claude_agents"` supports
  plan, apply, and destroy with optimistic version checks, archive-aware drift
  handling, classified retries, and API-key auth (KAS-38)
- Codegen target `eve` (Vercel eve, TypeScript): `kastor build` emits one
  eve project per root agent — `agent.ts` model config, `instructions.md`
  with the IO contract as convention, referenced agents as subagent
  directories, MCP tools as allow-listed connection files (endpoint URLs
  from `KASTOR_MCP_<SERVER>_URL`), http/runtime tools as `defineTool` files
  with Zod schemas. Both example modules build for it (KAS-32)

  Generated projects pin `eve@0.11.4` and the exact `ai` peer it requires;
  the shapes emitted were validated against that release's own type
  declarations. Models route through the Vercel AI Gateway, so the credential
  is `AI_GATEWAY_API_KEY` (or project OIDC on Vercel) rather than a
  per-vendor key, and `provider = "ollama"` is an error on this target — the
  gateway cannot route a local runtime. Model `params` are not emitted:
  eve 0.11.4 has no authored surface for sampling parameters, and the
  generated `README.md` says so.
- Generated LangGraph projects fail fast and lint clean (KAS-29): missing MCP
  configuration is caught at startup by `mcp_support.ensure_config()` naming
  what it looked for and where, instead of surfacing deep inside a run at
  tool-call time; an unknown tool URI now lists the tools the server actually
  advertises, so a typo is a one-glance fix; and every generated public
  function carries a return-type annotation, so `ruff` and `pyright` report
  the generated project clean

### Fixed

- `kastor plan` no longer green-lights a module the target cannot apply. A
  resource absent from state never reached the provider, so a spec with an
  unsupported tool source kind, a non-anthropic model provider, an unsupported
  model param, or a missing `KASTOR_MCP_<SERVER>_URL` planned clean as
  `+ agent.x (not in state)` and exited 0, then failed part-way through apply.
  Planned creates are now validated through the provider's `Diff` against an
  absent remote and fail the plan naming the resource and the target. The
  provider contract states what `Diff` must do when the remote object does not
  exist; `kastor validate` itself stays provider-agnostic (KAS-55)

- `kastor build` no longer overwrites an implemented `runtime` tool stub. Such
  a stub is generated once and then belongs to the user: the file is written
  only while it still matches the stub the last build wrote, is kept rather
  than deleted when its tool leaves the spec, and a spec change that lands
  under an implementation arrives as a `<name>.kastor-new` sidecar beside it
  plus a one-time warning naming both files (the build still succeeds, exit 0).
  Ownership is recorded in the output
  directory's `.kastorbuild` marker; a directory with no record is treated as
  the user's. Both codegen targets (langgraph, eve) are covered (KAS-24)

  **Upgrade note.** `.kastorbuild` was a one-line marker and is now a versioned
  manifest recording, per stub, the hash of what kastor last wrote and whether
  you have edited it. An output directory built by an older kastor has no
  records, so the first build after upgrading has no history: an untouched stub
  is still byte-identical to the fresh one and is recognized as kastor's, but a
  stub you implemented is treated as yours — correct, and the only direction
  that cannot destroy your code — and arrives with one `.kastor-new` sidecar and
  one warning, because kastor cannot tell your edits from a spec change. Diff
  the pair, delete the sidecar; from that build on the manifest has real records
  and it does not recur.

### Changed

- v0 platform provider selected: Claude Managed Agents. `target
  "claude_agents"` is the platform target label, authenticating from
  `ANTHROPIC_API_KEY`; the provider lands in `internal/provider/claude/`.
  Bedrock AgentCore and Dify are no longer under consideration (KAS-37)
- Docs audited against the shipped code (KAS-41). README, the docs site, and
  the Mintlify authoring rules no longer describe hosted providers as planned.
  Added: a worked `claude_agents` quickstart (module, `ANTHROPIC_API_KEY`,
  `KASTOR_MCP_<SERVER>_URL` per MCP server, plan/apply transcript); the
  irreversibility of `kastor destroy` on that target, which archives the agent
  and leaves it listed in the Console forever; what the target rejects — tool
  sources `http`, `script`, and `runtime`, model params other than `speed`, a
  non-`anthropic` model provider — and that `input`/`output` blocks have no
  counterpart in the remote object; and the fact that all of those are
  apply-time errors, since `kastor validate` is target-agnostic and `plan`
  calls no provider for a resource that is not yet in state

  (KAS-55 later moved those errors from apply to plan; the docs were updated
  to match in the same change.)
- Language reference completed (KAS-30). The page already listed every block
  type and field in SPEC.md §3; it now also carries the validation rules
  behind them — unknown attributes and duplicate block names are errors, the
  `source.uri` required/forbidden matrix, the absence of an `optional`
  attribute on tool params, prompt frontmatter and body rules, and which
  fields are errors on which target type. Each rule was verified against the
  shipped binary
- SPEC.md §3.1's provider table described only the LangGraph mapping and was
  never updated when the eve target shipped. It is now the real per-target
  support matrix with the credential per row, matching the language
  reference (KAS-32)

## [0.1.2] - 2026-07-17

### Fixed

- Homebrew cask publishing: the tap `token` field must be exactly
  `{{ .Env.VAR_NAME }}` — GoReleaser rejects any other interpolation, so
  v0.1.1 also shipped binaries but no cask (KAS-40)

## [0.1.1] - 2026-07-17

### Fixed

- Homebrew cask publishing: the tap token template used a function that does
  not exist in OSS GoReleaser, failing the release at publish time — v0.1.0
  shipped binaries but no cask (KAS-40)

## [0.1.0] - 2026-07-17

### Added

- `kastor init` — new-project scaffolding: a minimal working module with one
  agent, one MCP tool, one prompt, a model, and a LangGraph codegen target (KAS-39)
- Built-in in-memory platform provider: `target "memory"` works with
  `plan` / `apply` / `destroy`, no credentials required (#17)
- Support-triage canonical example — single agent, pure prompt (KAS-46)
- Documentation site at [docs.getkastor.dev](https://docs.getkastor.dev) and
  project landing page, including an installation page (KAS-40)
- `kastor version` reports the release version and commit; binaries are
  stamped at build time and `go install` builds fall back to Go build info (KAS-40)
- Homebrew install path: `brew install weirdGuy/tap/kastor`, published as a
  cask on release (KAS-40)
- This changelog (KAS-40)

### Changed

- SPEC.md realigned to the current agent-platform landscape, with a docs
  consistency sweep to match (#56, #58)
- Releases ship five binaries — darwin arm64/amd64, linux arm64/amd64,
  windows amd64; windows arm64 dropped (KAS-40)

### Fixed

- `internal/build` tests no longer fail when developer-local artifacts exist
  in example output directories (KAS-35)

## [0.0.1-alpha] - 2026-07-08

### Added

- HCL language core: `.agent`, `.tool`, and `.prompt` files plus `kastor.hcl`
  project files parsed into typed structs with aggregated diagnostics
- Module loading: directory walk, symbol table, cross-file reference
  resolution, prompt variables checked against agent inputs/outputs
- Dependency graph: DAG construction, cycle detection, deterministic
  topological sort
- `kastor validate` — full parse/resolve/graph pipeline
- `kastor build` — codegen engine with a LangGraph target (single agent with
  tools)
- `kastor plan` / `kastor apply` / `kastor destroy` — three-way
  spec/state/remote comparison, per-operation state persistence,
  `kastor.state.json` with versioning and a local lock file
- `kastor fmt` — canonical formatting via hclwrite
- Weather example module
- Release automation: GoReleaser + GitHub Actions on `v*` tags
- Apache License 2.0

[Unreleased]: https://github.com/getkastordev/kastor/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/getkastordev/kastor/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/getkastordev/kastor/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/getkastordev/kastor/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/getkastordev/kastor/compare/v0.0.1-alpha...v0.1.0
[0.0.1-alpha]: https://github.com/getkastordev/kastor/releases/tag/v0.0.1-alpha
