# Kastor — v0 Design Spec
 
> Declarative language for defining, compiling, and managing AI agents.
> "Terraform for agents": one spec → codegen for frameworks *or* lifecycle management on hosted platforms.
 
Status: **draft v0** · Syntax: **HCL** · Implementation: **Go**
 
---
 
## 1. Vision
 
Agents today are defined imperatively inside frameworks (LangGraph, CrewAI) or clicked together in platform UIs (Dify). There is no vendor-neutral, versionable, reviewable source of truth. The first generation of managed agent platforms (OpenAI Assistants, Bedrock Agents) was deprecated within ~3 years of launch — evidence that agent definitions need a vendor-neutral, versionable home that outlives any one platform.
 
Kastor provides:
 
1. **A spec** — typed, declarative definitions of agents, tools, prompts, and models.
2. **A compiler** — generate runnable projects for target frameworks (`kastor build`).
3. **A reconciler** — create/update/destroy agents on hosted platforms with plan/apply/state semantics (`kastor plan`, `kastor apply`).
Non-goals (v0): being a runtime, executing agents, evaluation/testing harnesses.
 
---
 
## 2. File Types
 
| Extension | Purpose | Contains |
|-----------|---------|----------|
| `.agent`  | Agent definition | model ref, prompt refs, tool refs, IO schema, deps |
| `.tool`   | Tool specification | interface (params, returns) + implementation source |
| `.prompt` | Prompt template | frontmatter (name, required variables) + raw body |
| `.kastor` / `kastor.hcl` | Project file | project meta, model blocks, targets, MCP servers, defaults |
 
All files in a directory tree form one **module** (like a Terraform module). Files reference each other by block address, not path.
 
---
 
## 3. Block Reference
 
### 3.1 `model` (in project file)
 
```hcl
model "fast" {
  provider = "openai"        # openai | anthropic | google | ollama | ...
  id       = "gpt-4o-mini"
  params {
    temperature = 0.2
    max_tokens  = 4096
  }
}
```
 
Vendor-neutral: agents reference `model.fast`, never raw model strings. Swapping providers is a one-line change. (`id` rather than `name` for the provider's model identifier — `name` is reserved for block labels conceptually.)

**Rules:**
- `params` is an open key/value bag — keys are provider-specific and are validated by the provider/target, not at parse time (a typo like `temperatur` is only caught by the platform; accepted v0 tradeoff). Whole-number values are integers, fractional values floats.
- Project files do not support expressions or references in v0 — attribute values must be literals.
- Unknown attributes and blocks are hard errors (strict in v0: loosening later is painless, tightening later breaks users).
- Duplicate block names within a file are a parse error. Module-wide (cross-file) duplicate detection is owned by module loading (see issue #6).

**Provider support is per target.** Any `provider` string parses — the block stays valid regardless, since a module may declare models several targets consume differently. A target that cannot map the provider fails when it consumes the block: `kastor build` for a codegen target, `kastor plan` for a platform target (a planned create is rendered through the provider, §5.2).

| provider | `langgraph` | `eve` | `claude_agents` |
|----------|-------------|-------|-----------------|
| `openai` | yes — `OPENAI_API_KEY` | yes — gateway namespace `openai` | error |
| `anthropic` | yes — `ANTHROPIC_API_KEY` | yes — gateway namespace `anthropic` | yes — `ANTHROPIC_API_KEY` |
| `google` | yes — `GOOGLE_API_KEY` | yes — gateway namespace `google` | error |
| `ollama` | yes — no credential (local runtime) | error — the Vercel AI Gateway cannot route a local runtime | error |
| anything else | error | error | error |

`eve` routes every model through the Vercel AI Gateway and pins it as `<namespace>/<id>`, so its credential is `AI_GATEWAY_API_KEY` (or project OIDC when deployed on Vercel) rather than a per-vendor key.

**LangGraph binding detail.** `langgraph` emits `init_chat_model("<prefix>:<id>", **params)`:

| provider | `init_chat_model` prefix | pip package |
|----------|--------------------------|-------------|
| `openai` | `openai` | `langchain-openai` |
| `anthropic` | `anthropic` | `langchain-anthropic` |
| `google` | `google_genai` | `langchain-google-genai` |
| `ollama` | `ollama` | `langchain-ollama` |

For LangGraph codegen, `params` keys must additionally be valid Python keyword arguments (`max_tokens`, not `max-tokens`); a key that is not is a codegen error. `claude_agents` accepts only `speed` and rejects every other key, including `temperature` and `max_tokens`, because the platform's model object exposes `id` and `speed` alone.
 
### 3.2 `agent` (.agent file)
 
```hcl
agent "weather" {
  description = "Answers weather questions for a location and date"
 
  model         = model.fast
  system_prompt = prompt.weather_system
 
  tools = [tool.web_search, tool.forecast_publish]
 
  # A human approves before this agent calls these. Everything else in
  # `tools` runs unsupervised.
  requires_approval = [tool.forecast_publish]
 
  input "location" {
    type        = string
    description = "The location to get the weather for"
  }
 
  input "date" {
    type     = string
    optional = true
  }
 
  output "weather" {
    type = string
  }
 
  # Implicit dependency: created because we reference agent.forecast
  input "forecast_context" {
    type    = string
    default = agent.forecast.output.summary
  }
 
  # Explicit fallback when no reference exists (rare)
  depends_on = [agent.geocoder]
}
```
 
**Ownership rules:**
- Agent owns the **model** and the **IO contract**.
- Prompt owns only its **template body** and the **variables it requires**.
- Validation: every variable a prompt requires must be satisfiable from the agent's inputs/outputs; conflict = compile error.
- `system_prompt` is **optional** (issue #7): an agent may omit it entirely, in which case the prompt-variable check is a no-op. When present it must be a `prompt.<name>` reference.
- An input `default` that references another agent's output (`default = agent.forecast.output.summary`) creates the dependency edge and is validated at compile time (the referenced output must exist), but **v0 codegen does not wire the data flow** — see §4.

**Tool approval.** `requires_approval` names the tools this agent may not call
without a human saying yes. It is the answer to "what can this agent do
unsupervised", stated where a reviewer reads it — in the diff, next to the
grant it narrows.

**`tools` is the grant; `requires_approval` narrows it.** The two lists are not
peers. A tool absent from `tools` cannot be called at all — that is the
denial, and every target implements it: `claude_agents` sets the toolset's
`default_config.enabled = false` and enables only the declared tools, `eve`
pins the declared tool names as the connection's allow-list, `langgraph`
passes exactly them to `create_agent`. A tool in `tools` is callable. A tool in
both is callable after a human approves. There is deliberately **no `deny`
value**: it would be a second, weaker spelling of omission, and the platform
API has no such policy to map it onto.

**Rules:**
- `requires_approval` is an optional list of `tool.<name>` references. Omitted
  (or an explicit `[]`) means every granted tool runs unsupervised.
- Every entry must also appear in `tools`. Approving a tool the agent was never
  granted is a compile error naming both lists — it reads as a grant and is
  not one.
- Duplicate entries are a compile error, as everywhere else.
- Entries are ordinary `tool.<name>` references and resolve like any other
  (§4). They create no *new* edge: a tool named here is already in `tools`.
- **The default is allow, and it is a decision, not an omission.** Declaring a
  tool is granting it; requiring approval by default would make `tools` mean
  nothing on its own. It would also fail closed *silently* — a platform agent
  deployed with every tool set to ask, and no human attached to its sessions,
  applies green and then cannot act. The fail-closed default already exists
  and is already explicit: leave the tool out of `tools`.

**Approval support is per target**, mirroring §3.1 and §3.3. Unlike those, it
is a **feature capability, not a binding** — see §3.3, "A declared target is a
promise", for what that distinction costs:

| | `langgraph` | `eve` | `claude_agents` |
|---|-------------|-------|-----------------|
| `requires_approval` | yes — `HumanInTheLoopMiddleware` plus `InMemorySaver` | yes — `needsApproval` on an authored tool, `approval` on an MCP connection | yes — the tool's `permission_policy` becomes `always_ask` |

On `langgraph`, `interrupt_on` names every granted tool: `True` for the
`requires_approval` subset and `False` for the rest. Human-in-the-loop requires
a checkpointer, so generated agents use `InMemorySaver`. That is intentional:
Kastor makes no promise about persisting tool state, and a durable checkpointer
is a memory-substrate question (§7; KAS-61). The generated project's
interrupt/resume entry-point contract remains separate work (KAS-67); approval
configuration does not redefine it implicitly.

### 3.3 `tool` (.tool file)
 
A tool is an **interface + implementation source**.
 
```hcl
tool "web_search" {
  description = "Search the web"
 
  param "query" {
    type        = string
    description = "The query to search for"
  }
 
  param "max_results" {
    type    = number
    default = 10
  }
 
  param "include_images" {
    type    = bool
    default = false
  }
 
  returns {
    type = string
  }
 
  # One implementation block per target; one unqualified block covers all:
  source {
    kind = "mcp"                       # mcp | http | builtin | runtime | script
    uri  = "mcp://search-server/web_search"
  }
}
```

Bindings may differ per target; identity and the param contract do not:

```hcl
tool "web_search" {
  description = "Search the web"

  param "query" { type = string }
  returns { type = string }

  source {
    kind    = "builtin"
    id      = "web_search"
    targets = [target.claude_agents]
  }

  source {
    kind    = "mcp"
    uri     = "mcp://search-server/web_search"
    targets = [target.langgraph, target.eve]
  }
}
```
 
**Source kinds:**
 
| kind | Meaning | Identifier |
|------|---------|------------|
| `mcp` | Tool served by an MCP server | `uri = "mcp://<server>/<tool>"` |
| `http` | REST endpoint (OpenAPI-style descriptor) | `uri` — the endpoint URL |
| `builtin` | Provided by target platform (e.g. the target platform's hosted web-search tool) | `id` — the platform's own name for the tool |
| `runtime` | Implemented in user code within the generated project (codegen emits a stub) | none — the generated stub is the implementation |
| `script` | Inline/local script executed by generated glue code | `uri` |

**Source-kind support is per target**, mirroring the model-provider matrix of §3.1. A tool block stays valid regardless of what any one target supports; a target that cannot bind the source *selected for it* (see binding selection, below) is an error.

| kind | `langgraph` | `eve` | `claude_agents` |
|------|-------------|-------|-----------------|
| `mcp` | yes | yes — surfaced through the server connection, no `tools/` file | yes — the server is sent as a URL connection |
| `http` | yes | yes | error |
| `runtime` | yes — generated stub | yes — generated stub | error |
| `builtin` | error, **permanently** — platform-provided tools have no local binding | error, **permanently** | yes — `id` must name a tool in the platform toolset |
| `script` | error — deferred (issue #36) | error — deferred (issue #36) | error |

This matrix belongs to the targets, not to the module: each generator and provider declares the source kinds it supports, and **`kastor validate` cross-checks every tool against every target the module declares** (§5). Targets never declare capabilities in HCL — the matrix is part of a target's implementation, and this table is its documentation.

**A declared target is a promise.** A module that cannot build or apply for a target it declares fails at `validate`, not at `build` or `plan` — which means a broken `langgraph` binding also stops `kastor plan` for a `claude_agents` target in the same module, since both commands run the same pipeline. That is the enforceable reading of "write once, target many": if a target is declared, the module is claimed to work there. A module that genuinely targets one destination declares one target; per-target `source` blocks are how a module keeps a target it would otherwise have to drop.

**The promise covers bindings, not every feature.** The rule above is
enforceable because it comes with an escape hatch: a tool a target cannot bind
can be given one with `targets`. Not every unsupported thing has that shape, so
the language distinguishes two cases by a single test — **can the target render
the resource at all, or only render it without honoring a modifier on it?**

| | Example | Where it fails | Blast radius |
|---|---------|----------------|--------------|
| **Binding** — the target cannot render the resource | `builtin` on `langgraph`: no tool function can be emitted | `validate`, module-wide | every command, every target |
| **Feature capability** — the resource renders, a modifier on it cannot be honored | a future modifier whose resource still renders correctly but whose behavior the target cannot preserve | `validate` **warns**; the consuming command errors for that target alone | that one target |

A feature capability therefore does **not** block `kastor plan` for a platform
target in the same module, and does not block `kastor build --target <other>`.
`kastor validate` reports it as a warning naming the agent, the target, and the
tools affected, and still exits 0 (§5) — the module is valid, one target cannot
honor all of it yet.

The reason the two differ is what the user can do about it. An unbindable tool
has a fix in the language; an unsupported feature has none short of dropping
the feature or the target, and a rule that can only be satisfied by deleting
something should not take the whole module down with it. The cost of the
distinction is that every future capability has to be sorted into one bucket;
the test above is how, and the answer belongs in the target's capability
descriptor, not in a bespoke rule per feature.

Validate checks only what is knowable from the spec. Facts that depend on the environment or on platform-side objects — an unset credential variable, a revoked connection, a tool the remote agent is not permitted to call — belong to `kastor doctor` (§5.3), so `kastor validate` still needs no credentials and no network, and neither does `kastor plan`.

**Codegen mapping (LangGraph target).** Applies to the source **selected for that target**:

| kind | Generated binding |
|------|-------------------|
| `mcp` | `@tool` function calling the named server tool through a generated MCP bridge, configured from the tool's `mcp_server` block (§3.6) |
| `http` | `@tool` function POSTing the tool's params as a JSON object to `uri` |
| `runtime` | `@tool` stub raising `NotImplementedError` until user code supplies the body |
| `builtin` | Unreachable: `builtin` has no local binding, and `validate` rejects a module that selects a `builtin` source for a codegen target |
| `script` | Unreachable for the same reason, **for now**: glue-code execution is deferred (issue #36) |

**Rules:**
- `kind` is a closed enum (like `target.type`): `mcp | http | builtin | runtime | script`. Unknown kinds are compile errors.
- **At least one** `source` block and exactly one `returns` block per tool. Zero `param` blocks is fine.
- A `source` block may declare `targets` — a list of `target.<name>` references — which restricts it to those targets. A `source` without `targets` is the tool's **default binding**; at most one per tool.
- **Binding selection.** For target T a tool binds the source whose `targets` names T, otherwise the default. The invariant is **exactly one binding per (tool, target)** — the same guarantee "exactly one `source`" used to give, one axis richer: two sources naming the same target is an error, and a tool with no binding for a declared target is an error. Both are reported by `kastor validate`, naming the tool, the target, and the agents that use the tool.
- `targets` entries are references to `target` blocks declared in the module; an unknown one is an error listing the declared targets. Unlike the references of §4 they create no graph edge — targets are not nodes.
- A tool's identity and contract are shared by all of its bindings: `description`, `param`, and `returns` are declared once and cannot vary per target. A tool whose parameters differ per target is two tools.
- Identity attributes are per-kind and exclusive. `uri` is required for `mcp`, `http`, and `script`, and an error on any other kind. `id` is required for `builtin` — it is the platform's own name for the tool (`id = "web_search"`), the same relationship `model.id` has to a `model` block's label — and an error on any other kind. `runtime` takes neither: the generated stub is the implementation. Meaningless fields are errors, not ignored.
- A `builtin` source's `id`, not the tool's block label, is what a platform resolves. The label stays a kastor address with no vendor meaning.
- A `runtime` stub is **generated once and then belongs to the user**: its body is user code, so `kastor build` writes the file only while it is still absent or byte-identical to the stub the last build wrote there, and never writes it again once the file has been edited. A spec change that a user's implementation cannot be merged into still has to reach them, so the build writes the new stub beside their file as a `<name>.kastor-new` sidecar and reports the pair; a stub whose tool leaves the spec entirely is kept, not deleted, and reported the same way. Each is reported once — the sidecar on disk is the durable message. Reverting a file to its current stub hands it back to kastor and clears the sidecar. Ownership is recorded in the output directory's `.kastorbuild` marker; a directory that has no such record is treated as the user's, since assuming otherwise is the only way to lose their work.
- For `mcp` sources the `uri` names a **declared server and a tool on it**: `mcp://<server>/<tool>`, where `<server>` must match an `mcp_server` block in the module (§3.6). An unknown server is a compile error naming the file, the tool's block address, and the servers the module declares. Like `target.<name>` the reference is resolved and validated but creates no graph edge (§4) — servers are not nodes. The `<tool>` segment is the server's own name for the tool and is not checkable without contacting the server, so a mismatch surfaces at run time, not at `validate`.
- **The langgraph target generates `mcp_servers.json`** from the module's `mcp_server` blocks — a generated artifact like every other file it writes: deterministic, marked do-not-edit, never hand-maintained. It holds connection config only; a credential value never appears in it, and neither does an `auth.ref`. Auth is injected by the generated bridge, which reads the environment in the user's own process at call time.
- `KASTOR_MCP_CONFIG` survives as a **local override**: pointing it at a file replaces the generated one wholesale for that run. It is a development escape hatch — for aiming a run at a local server instance — not how connection details are meant to arrive. It carries no validation: a server the override omits or renames fails at the call, not at build. It exists only on the codegen path, where there is no state file and no plan for a bad override to corrupt.
- Param and returns types are bare keywords, not strings: `type = string`, never `type = "string"`. Closed enum in v0: `string | number | bool` (compound types deferred to v1).
- `default` must be a literal whose type matches the declared `type`; a mismatch or an explicit `default = null` is a compile error. A param with a `default` is optional at call time; there is no separate `optional` attribute on tool params.
- `description` is optional on both the tool and its params at parse time (targets may enforce more).
- A `.tool` file may contain multiple `tool` blocks; duplicate names within a file are a parse error (module-wide duplicates are owned by module loading, issue #6).
 
### 3.4 `prompt` (.prompt file)
 
Frontmatter + body. Body is the raw prompt; variables use `{{var}}`.
 
```
---
name = "weather_system"
requires = ["location"]
---
You are a weather assistant. The user is asking about {{location}}.
Answer concisely using the provided tools.
```
 
No model, no IO — prompts are pure templates (per decision #2).

**Rules:**
- Frontmatter starts at byte 0: the opening `---` must be the first line of the file, and a closing `---` line is required. The interior is HCL (so `#` comments work there), allowing exactly two attributes: `name` (required string) and `requires` (optional list of string). Unknown attributes are hard errors.
- Variable grammar: `{{ident}}` where `ident` matches `[A-Za-z_][A-Za-z0-9_]*`, with optional whitespace inside the braces (`{{ date }}`). Any other brace sequence — `{{1bad}}`, `{{not-a-var}}`, a lone `{{` — is literal body text, not an error, so prompts can embed JSON and code examples freely. There is no escape syntax for a literal well-formed `{{var}}` in v0.
- `requires` is an optional contract. Omitted → the variable set is inferred from the body. Present (including an explicit `requires = []`) → it must match the body's variables exactly, both directions: a body variable missing from `requires` and a `requires` entry never used in the body are both compile errors. Duplicate entries are a compile error.
- An empty (or whitespace-only) body is a compile error — there is no valid use for a zero-byte prompt.
- The body is everything after the closing delimiter line, preserved byte for byte.

### 3.5 `target` (project file)

Target implementations are external plugins. A module declares their stable
source and version separately from target instances:

```hcl
kastor {
  required_plugins {
    langgraph = {
      source  = "github.com/getkastordev/kastor-langgraph"
      version = "~> 0.1"
    }
    anthropic = {
      source  = "github.com/getkastordev/kastor-anthropic"
      version = "~> 0.1"
    }
  }
}

# Codegen target instance → `kastor build`
target "python" {
  type   = "codegen"
  plugin = "langgraph"
  output = "./gen/langgraph"
}

# Managed platform target instance → `kastor plan` / `kastor apply`
target "production" {
  type   = "platform"
  plugin = "anthropic"

  config {
    api_key_env = "ANTHROPIC_API_KEY"
    vault_id    = "vlt_011CZkZDLs7fYzm1hXNPeRjv"
  }
}
```

**Rules:**
- `kastor.required_plugins` maps a module-local name to a stable `source` and a
  version constraint. Both strings are required and unknown fields are errors.
  The local name is configuration syntax only. Installation selects a version;
  state ownership uses the canonical source, with resolved version/protocol
  recorded as metadata (§5.1). A compatible version upgrade is not a change
  of provider identity.
- `target.plugin` names one entry in `required_plugins`. A missing entry is a
  compile error listing the declared plugins. During the v0.2 migration window,
  an omitted selector is accepted as a deprecated compatibility form and the
  target label is resolved through the legacy in-process adapter. New modules
  and generated scaffolds always use the explicit form.
- `kastor init` resolves version constraints against GitHub releases and writes
  `.kastor.lock.hcl`: source, exact version, protocol, platform asset mapping,
  and publisher-provided SHA-256 checksums. The lock is deterministic and must
  be committed. `--upgrade` refreshes selections, `--frozen` rejects any lock
  change, and `--offline` forbids network access.
- Explicit plugins are executable processes. Core resolves an explicit
  development override first, otherwise the verified lock-selected cache;
  `PATH` is only a migration fallback for a module with no lock. It performs a
  protocol/source/version/kind handshake and delegates validation plus the
  target operation over the versioned protocol. A present lock is authoritative:
  checksum failure is rejected before execution and never falls through.
- The final source path segment is the executable name. v0 installation
  supports `github.com/<owner>/<repository>` sources and their release assets;
  no public Kastor registry is required.
- Scaffold content is an optional plugin protocol capability. `kastor new`
  installs and locks the selected plugin, then requests its deterministic
  starter files. Core validates paths and owns disk writes but contains no
  framework-specific starter template.
- A target's label is only its module-local instance identity. Multiple target
  blocks may select the same plugin with different output paths or config.
- `type` is a closed enum: `codegen` or `platform`. Unknown values are a compile error; new target types are additive spec changes.
- `codegen` targets require `output`. `platform` targets do not allow it.
- `config` is an optional literal-only map owned and validated by the selected
  plugin. Core preserves it as a JSON-compatible value tree and has no
  provider-specific fields. Unknown or meaningless config entries are plugin
  errors rather than silently ignored values.
- The built-in memory target is an **ephemeral in-memory store** for examples,
  onboarding, and CI. It accepts no config. Its remote objects die with the
  process, so a later invocation truthfully reports remote-missing drift.
- The Anthropic plugin accepts `api_key_env` and `vault_id` in `config`.
  `vault_id` is required when an MCP server bound to that target uses a
  `connection://` reference. It names a location, not a secret; only plugin
  readiness checks read enough to verify the reference. Plan and apply never
  read credential values.

### 3.6 `mcp_server` (project file)

An MCP server the module's tools bind to. Declaring the server is what makes
`mcp://<server>/<tool>` resolvable (§3.3).

```hcl
# Remote server; the target platform holds the credential
mcp_server "hubspot" {
  url = "https://mcp.hubspot.com"
  auth {
    ref = "connection://cred_011CZkZDLs7fYzm1hXNPeRjv"
  }
}

# Remote server; the credential is in the environment
mcp_server "airtable" {
  url = "https://mcp.airtable.com/mcp"
  auth {
    ref = "env://AIRTABLE_TOKEN"
  }
}

# Local server, spawned by the generated project
mcp_server "fetch" {
  transport = "stdio"
  command   = "uvx"
  args      = ["mcp-server-fetch"]
}
```

**Rules:**
- `transport` is a closed enum — `http` | `stdio` — defaulting to `http`. Unknown values are compile errors.
- `http` requires `url` and allows `auth`. `stdio` requires `command`, allows `args` (list of string, default `[]`), and allows no `auth`: a spawned local process inherits the environment that spawned it. Fields meaningless for a transport are errors, not ignored (§3.5's stance).
- Project files carry no expressions (§3.1), so `url`, `command`, and `args` entries are literals.
- Duplicate names within a file are a parse error; module-wide duplicates are owned by module loading, like every other block.
- A declared server no tool references is not an error, the same as an unreferenced `model`. A platform is sent only the servers an agent's tools actually bind.

**Transport support is per target**, mirroring §3.1 and §3.3:

| transport | `langgraph` | `eve` | `claude_agents` |
|-----------|-------------|-------|-----------------|
| `http` | yes | yes | yes |
| `stdio` | yes — spawned by the generated project | error — the connection is an HTTP client; a stdio-only server needs an HTTP bridge in front of it | error — the platform dials a URL |

**Credential references.** `auth.ref` is a URI naming *where a credential lives*.
The scheme set is closed in v0:

| scheme | Meaning | `langgraph` | `eve` | `claude_agents` |
|--------|---------|-------------|-------|-----------------|
| `env://NAME` | The value of environment variable `NAME`, read by whatever dials the server | yes — the generated bridge sends `Authorization: Bearer` from `NAME` at call time | yes — the same, from the connection's headers callback | error — the platform's agent object accepts no credential; use `connection://` |
| `connection://<credential_id>` | A credential the target platform already holds, authenticated out of band | error — no platform holds connections on the codegen path | error | yes — kastor sends the server's name and URL only; `kastor doctor` verifies the credential (§5.3) |

- **Kastor is never the credential holder.** It implements no OAuth flow, stores no token, and refreshes nothing. Brokering a token exchange would require a durable secret store, and the only one kastor has is a plaintext state file (§5.1); it would also make `kastor plan`, defined as a pure read (§5.2), an operation that must refresh tokens. Obtaining and refreshing a credential belongs to the environment (`env://`) or to the platform (`connection://`). This is a permanent non-goal, not a v0 deferral.
- **In v0 kastor never reads a credential value.** Each supported (scheme, target) pair is resolve-free: on the codegen path the generated project reads `env://` itself at call time; on the platform path `connection://` is matched by the platform against its own store. The rejected pairs are exactly the ones that would force kastor to read a secret and transmit it.
- **State records the ref, never the resolved value** (§5.1). Changing `env://A` to `env://B` is a visible diff; rotating the secret behind `env://A` is invisible to kastor — correct, because kastor does not manage the secret.
- An unknown scheme is a compile error listing the known ones, so new resolvers are additive (§7).
- `auth` is optional. A server without it is unauthenticated on every target.
- An `auth` block may declare `targets` — a list of `target.<name>` references — exactly as a tool's `source` block does (§3.3). A block without `targets` is the server's default. Two `auth` blocks naming the same target is an error, as is more than one default. A server with **no** `auth` block at all is unauthenticated everywhere, which is the normal case for a public or local server; but once a server declares any `auth` block, every target it is bound on must have a binding — otherwise a server picked up authentication on one path and silently lost it on another. All three are reported by `kastor validate`, naming the server, the target, and the tools bound to it.
- Per-target `auth` is not an optimization. `env://` is codegen-only and `connection://` is platform-only by construction, so a module targeting both paths — the §8 milestone-4 shape — cannot express an authenticated server without it.

```hcl
mcp_server "hubspot" {
  url = "https://mcp.hubspot.com"

  auth {
    ref     = "connection://cred_011CZkZDLs7fYzm1hXNPeRjv"
    targets = [target.claude_agents]
  }

  auth {
    ref     = "env://HUBSPOT_TOKEN"
    targets = [target.langgraph, target.eve]
  }
}
```

**On `claude_agents`, `connection://` is verified by `kastor doctor`** (§5.3),
not by `plan`. The ref names a credential **id** in the vault the target declares
(`config.vault_id`, §3.5) — not a display name, which the platform allows to be absent
and to repeat. `doctor` fetches it and reports a finding if it does not exist, if
it is archived, or if the credential's own MCP server URL does not equal this
block's `url`. So a typo'd, archived, or misdirected credential is named by a
readiness check rather than discovered at the agent's first tool call. Kastor
still sends only the server's name and URL: the credential stays on the platform
and is never read.

The check lives in `doctor` and not in `plan` for two reasons. `plan` is a pure
read of spec/state/remote whose whole output vocabulary is pending change, and a
credential fault is not a pending change — there is no create, update, or delete
that fixes it (§6). And the verification is inherently racy: a credential valid
at `plan` can be revoked before `apply`, so plan-time verification buys an
assurance it cannot deliver. `doctor` makes no such promise — it reports what was
true when it ran.

**Where connection config lives.** Earlier drafts kept MCP transport entirely out
of the spec: connection details were deployment configuration. That holds on the
codegen path, where kastor's output is a project someone deploys into an
environment with its own config layer. It does not hold on the platform path,
where `kastor apply` *is* the deployment and a server's address has nowhere to
come from but the spec. Reading it from the environment instead made a target's
desired config depend on the operator's shell, wrote an environment-derived value
into the state file, and left drift on that attribute comparing one shell against
another. The line now sits here: a server's **identity, address, and the location
of its credential** are spec; a credential's **value** never is. On the codegen
path only, the generated runtime config may still be overridden locally (§3.3,
`KASTOR_MCP_CONFIG`).
 
---
 
## 4. Dependency & Reference Semantics
 
- **References create the DAG.** `agent.forecast.output.summary` makes `weather` depend on `forecast`. Same rule for `model.*`, `tool.*`, `prompt.*`. Two references are exceptions — `target.<name>` (`source.targets` and `auth.targets`, §3.3 and §3.6) and the server segment of an `mcp://` uri (§3.6): both are resolved and validated like any other reference but create no edge, because neither targets nor MCP servers are nodes in the agent graph.
- **References order and validate; v0 codegen does not move data.** A cross-agent reference is checked at compile time (the output must exist) and orders the DAG, but generated code exposes the referencing input as an ordinary caller-supplied parameter — the caller runs the upstream agent and passes the value. Wiring actual data flow is orchestration, deferred to v1 (§7).
- `depends_on` is the explicit escape hatch for ordering without data flow (Terraform-style).
- Cycles are a compile error.
- Cross-module references (v1): `module.<name>.agent.<name>` — registry/import system deferred.
---
 
## 5. CLI
 
| Command | Function |
|---------|----------|
| `kastor new [--from SOURCE] [dir]` | Create a module from a plugin-owned scaffold |
| `kastor init [--upgrade] [--frozen] [--offline] [dir]` | Resolve, verify, install, and lock executable plugins |
| `kastor validate` | Parse + type-check + resolve references + check every tool against every declared target, warning on features a declared target cannot honor yet (§3.3) |
| `kastor build [-target X]` | Codegen for framework targets |
| `kastor plan` | Diff spec vs. state file vs. remote platform |
| `kastor apply` | Reconcile platform targets, update state |
| `kastor doctor` | Readiness check: can what is deployed actually run (§5.3) |
| `kastor destroy` | Remove managed remote agents |
| `kastor fmt` | Canonical formatting |
 
Exit codes (all commands): 0 clean, 1 validation/codegen/plan/apply errors, 2 usage/IO errors (including lock contention). **Warnings never change an exit code.** They are diagnostics with a severity, not soft failures — a module that warns is a module that is valid. Every command's diagnostics carry that severity (§5.2 already relies on it for drift), so the `--json` rendering of §9 reports warnings without a second convention.

### 5.1 State file

`kastor.state.json`, at the module root, records what `kastor apply` manages: block addresses → remote resource IDs, plus the configuration last applied to each.

```json
{
  "version": 2,
  "serial": 4,
  "targets": {
    "claude_agents": {
      "plugin": {
        "source": "github.com/getkastordev/kastor-anthropic",
        "version": "0.1.0",
        "protocol": 1
      },
      "resources": {
        "agent.weather": {
          "id": "agent-abc123",
          "config": { "model": { "id": "claude-sonnet-4-5", "provider": "anthropic" } },
          "dependencies": ["agent.geocoder"]
        }
      }
    }
  }
}
```

**Rules:**
- `version` is the state format version. Core reads v1 and v2, writes v2, and rejects all other versions without rewriting them. `serial` increases by one on every successful atomic write, ordering snapshots; failed writes do not advance it.
- Each managed target records `plugin.source`, the canonical implementation owner, and its resolved `version` and `protocol`. Executable metadata comes from the validated handshake of the resolved binary (normally the lock-selected artifact; development overrides and the no-lock PATH fallback record the actual resolved version). A plugin alias rename or a supported version upgrade for the **same source** does not change ownership or itself produce a remote operation. The existing resolver still enforces the module version constraint and supported protocol. The provider remains responsible for config compatibility.
- A different source while resources remain managed is an error before lifecycle calls, including `destroy` and `doctor`. There is no source-rebinding command. Destroy using the original owner before switching; empty targets are dropped on write. Managed targets removed from the module, or changed to codegen, require restoring the original platform declaration. These checks cover all managed targets, even with `--target`, because state writes replace the whole snapshot.
- **v1 migration** binds each managed target to its explicit `target.plugin` / `required_plugins` declaration, preserving labels, remote IDs, dependencies, and last-applied configs. A missing selector/declaration is ambiguous and rejected. The legacy `claude_agents` label must select `github.com/getkastordev/kastor-anthropic`. For other explicit target labels, the operator's declaration asserts the original source: v1 contains no source to independently verify. The built-in selector-free `memory` target is the exception: it binds to `builtin/memory`, version `builtin`, protocol `0`; an explicit external plugin cannot reinterpret v1 memory resources. Its remote store remains ephemeral, so later remote-missing drift is real, not migration churn.
- New state from in-process compatibility adapters records their canonical source and version `builtin`, protocol `0` (Claude uses the same official source as its executable successor). Existing non-memory v1 state must first adopt explicit plugin declarations; the legacy selector alone cannot migrate it.
- Migration and metadata refresh happen in memory during preparation. **Plan and doctor never persist them.** Apply persists them with the first successful resource operation or stale-config refresh, or once after a successful no-op apply. Destroy persists them with each successful deletion. Failure before any successful state mutation leaves the original file untouched; a successful prefix of a partial apply/destroy is saved as v2. An empty destroy does not write. A format-only upgrade never creates, updates, or deletes remote objects, and equivalent legacy normalized configs are retained using the provider's pure `Diff` comparison.
- The **unit of remote management is the agent**: each `agent` block is one resource; its model, prompt, and tools are folded into the resource's config (the "agent closure"). Standalone remote tool/prompt objects are deferred.
- `config` is the **full last-applied config** (canonical JSON, not a hash) — drift reports can then name the attributes that changed without refetching anything.
- **Credential references are stored; credential values never are.** A resource's `config` records an `auth.ref` (§3.6) verbatim — `env://AIRTABLE_TOKEN`, not the token behind it. Nothing in `kastor.state.json` is a secret, and no plan rendering of it can leak one. This is what keeps the state file safe to hand to a colleague while debugging, and it holds for every resolver added later.
- `dependencies` records the resource's managed (agent) dependencies so a resource that has been removed from the spec can still be destroyed in reverse dependency order — the module graph no longer knows it.
- Serialization is deterministic: stable key order, byte-identical output for equal state. Writes are atomic (temp file + rename) and happen **after every applied operation**, not once at the end — an interrupted apply loses nothing, and a re-run plans exactly the remainder.
- Like Terraform state, the file is environment-specific and is not meant to be committed.

**Locking:** plan/apply/destroy/doctor take a local lock file (`.kastor.state.lock`) while inspecting or mutating state; contention errors name the holding pid and the recovery step (delete the stale file only after confirming the process has stopped). Remote state backends and remote locking are deferred (§7).

**Backup and recovery:** migration does not make an automatic backup. Back up the
state, module declarations, and plugin lock before the first mutating command.
Older v1-only binaries cannot read v2. Never restore an old snapshot over a
successful partial apply: it would lose the IDs and progress already recorded.
See [STATE_MIGRATION.md](STATE_MIGRATION.md) for the pre-release procedure and
failure recovery.

### 5.2 Plan/apply semantics

`kastor plan` is a **three-way comparison** — spec vs. state vs. remote — and a **pure read**: it issues only `Read`/`Diff` provider calls and never touches the state file.

Per resource, in the module's topological order (deletes first, in reverse dependency order):

| Situation | Plan |
|-----------|------|
| in spec, not in state | create ("not in state") |
| in state, remote object missing | create ("remote object … missing") + drift warning |
| in spec and state, remote differs from spec | update, with attribute-level diffs |
| in spec and state, remote matches | no-op |
| in state, not in spec | delete ("removed from spec") |

**Creates are validated, not assumed:** for every planned create the engine calls the provider's `Diff` against an absent remote (§6), so a spec the target cannot express — an unsupported tool source kind, a foreign model provider — is a plan error naming the resource, not an apply-time surprise.

**Drift** (remote changed outside kastor) is detected by diffing the *last-applied* config against the remote and reported as a warning naming the changed attributes; apply converges the remote back to the spec. When the user instead edits the spec to match a manual remote change, the plan is a no-op and apply silently refreshes the stale state entry (no remote call), so the warning does not recur.

**Plan output.** One line per pending change, in execution order: `+` create, `~` update, `-` delete, with the reason in parentheses on creates and deletes, and one indented `path: old → new` line per attribute diff under updates (values render as compact JSON, truncated so a prompt body cannot flood the plan). An attribute diff is `{path, old, new}` with dotted paths (`model.id`, `tools[0].source.uri`); `old` is null when an attribute is being added, `new` null when it is being removed. Warnings precede the summary. The summary line is countable and per-target — `Plan for target.<name>: N to create, M to update, K to delete, J unchanged.` — or `No changes for target.<name>: remote matches the spec (N resources).` when nothing is pending. The whole plan (target, ordered changes, attribute diffs, diagnostics) is one serializable tree, so the `--json` rendering (§9) is a second renderer over the same data, not a second pipeline.

`kastor apply` executes the plan in order and stops at the first failure; everything applied up to that point is already saved in state, and the error states what failed, what had been applied, and — if a resource was created remotely but saving state failed — the remote id, so nothing is orphaned silently. `kastor apply` does not prompt for confirmation in v0. `kastor destroy` deletes everything in state in reverse dependency order.

Diagnostics from plan/apply are structured (severity, block address, summary, detail) so a machine-readable `--json` rendering (§9) is a renderer, not a redesign.

### 5.3 Readiness (`kastor doctor`)

`plan` and `apply` are about **declared configuration**: they answer "does the
remote match the spec". They do not answer "can the thing that is deployed
actually run". An agent whose MCP connections are unauthenticated and whose tool
permissions deny everything matches its spec exactly — `plan` clean, no drift —
and cannot serve a single request. Readiness is a different question and gets its
own verb.

```
kastor doctor [--target <name>] [dir]
```

`kastor doctor` is **read-only**: it never invokes an agent, never mutates a
remote object, and never writes the state file. For every agent the state tracks
on the selected platform target(s) it reports:

| Check | What it establishes |
|-------|---------------------|
| **Remote existence** | The state's id still resolves to a live object (provider `Read`) |
| **Credential verification** | Each `connection://` ref bound on this target names a credential that exists, is not archived, and whose own MCP server URL equals the declaring `mcp_server` block's `url` (§3.6) |
| **Environment readiness** | Which `env://` refs the module needs are currently unset |
| **Tool permissions** | The remote grants every tool the agent's `tools` list declares, and gates exactly what `requires_approval` names (§3.2) |

**Three outcomes, not two.** Every check reports `ok`, `failed`, or `unknown`,
and the third is load-bearing: **"could not verify" is never rendered as
"missing"**. An unreachable vault and an absent credential are different facts
and a user acts differently on each — one is an outage to wait out or a network
path to open, the other is a spec or platform change. Collapsing them would make
`doctor` actively misleading in exactly the situation where a user most needs to
trust it. `unknown` counts as a finding (the command did not establish
readiness), and is counted separately from `failed` in the summary.

**Credential ids are printed with their display name alongside** —
`cred_011CZkZDLs7fYzm1hXNPeRjv ("HubSpot Prod")`. The id is the identifier
because a display name is nullable and non-unique on the platform and therefore
cannot be one (§3.6); that is a correctness constraint, not a reason for output
to be unreadable. A credential with no display name prints as its id alone.

**Environment readiness needs no platform at all.** It compares the module's
`env://` refs against the process environment, so it answers *"what does this
module need from my environment before it will run"* offline, with no
credentials and no network — the same standing that `validate` has.

**Output follows the plan renderer's conventions** (§5.2): one line per finding,
structured diagnostics carrying severity, block address, summary, and detail, and
a countable per-target summary line. The whole report — target, per-resource
checks, diagnostics — is **one serializable tree**, so the `--json` rendering of
§9 is a second renderer over the same data rather than a second pipeline.

Exit codes follow §5: **0** ready, **1** findings, **2** usage/IO.

`doctor` is deliberately not a step inside `plan` or `apply`. Its checks are racy
by nature — a credential valid when it ran can be revoked a second later — so
folding them into `apply` would promise an assurance neither command can keep,
while costing `plan` its purity (§6). `doctor` promises only to report what was
true when it ran, which is a promise it can keep.
 
---
 
## 6. Architecture (Go)

The canonical core Go module is `github.com/getkastordev/kastor`; the public
plugin contract is `github.com/getkastordev/kastor/protocol/v1`. The repository
transfer and first new-path publication are separate gates (see
[RELEASING.md](RELEASING.md)). A Go import-path change does not change protocol
version 1, plugin source identity, or module lock/state identity.

Core owns language, graph, lifecycle, state, plugin installation, and
deterministic writes. External plugins own capabilities, target configuration,
framework mappings, hosted API clients, and scaffolds. The in-process target
packages below remain **legacy compatibility adapters** during the v0.2
migration window in §3.5, not the extension point for new target work.

```
cmd/kastor/         CLI (cobra)
protocol/v1/       public neutral IR and executable JSON RPC contract
internal/
  parser/           HCL decode (hashicorp/hcl/v2) → AST
  schema/           typed config structs, validation
  module/           directory walk → symbol table, cross-file reference resolution
  graph/            DAG construction, cycle detection, topo sort
  build/            codegen engine
    langgraph/      legacy LangGraph adapter (Python)
    eve/            legacy eve adapter (TypeScript)
  provider/         platform reconcilers
    memory/         built-in in-memory platform (demos, examples, CI)
    claude/         legacy Claude Managed Agents adapter
  plugin/           installation, verification, discovery, RPC adapters
  state/            state file read/write, locking, diff
```
 
Providers implement a common interface (`Read/Create/Update/Delete/Diff`).
Explicit targets already use executable plugins over protocol-v1 JSON messages
on stdin/stdout; they do not use an in-process Go registry or gRPC. The public
protocol contains neutral types, never imports from core `internal/` packages.

**Provider contract** (`internal/provider`): the engine renders each agent's closure into a neutral, serializable config (a JSON value tree — no core Go types, no provider types), so the interface can move behind a plugin boundary without redesign. Contract rules:

- `Read(id)` reports found=false for a resource deleted outside kastor — that is drift data, not an error.
- `Create(resource)` returns the platform's id; the engine records it in state immediately.
- `Delete(id)` is idempotent: deleting an already-missing remote object succeeds, so re-runs after partial failures converge.
- `Diff(desired, remote)` is the comparison authority — only the provider knows how the neutral config maps onto its platform's attributes. Empty result = in sync. The engine also diffs the last-applied config against the remote for drift detection.
- `Diff` must accept a **nil remote**, meaning the object does not exist on the platform. It then validates the desired config exactly as it would against an existing object — returning an error is how a provider rejects a spec it cannot map — and otherwise returns one attribute diff per attribute a `Create` would set. The engine calls `Diff` this way for every planned create, so a module that cannot apply fails at plan.
- `Diff` must be pure and deterministic; `Read` must not mutate. `kastor plan` issues only these two. In particular `Diff` issues **no reads of its own**: an earlier draft let it verify a `connection://` credential against the target's vault during plan, and that is reverted. `Diff`'s entire output vocabulary is drift, the verification's own constraint was that a failure is never rendered as drift, and a check that cannot produce drift does not belong in the drift function. Keeping it there cost provider-contract purity for every future provider, offline `plan`, in-memory-provider coverage of the plan path, and would have needed a `--skip-verify` flag for network-restricted CI — needing a switch to disable a check is the tell that it sits in the wrong place. Readiness verification is `kastor doctor`'s (§5.3).

**Optional capabilities.** A provider may implement interfaces beyond the five above; the engine feature-detects them and degrades to the base contract when they are absent. `Checker` (§5.3) is the first. An optional capability may relax the rules above **only where the verb invoking it has no contract to relax** — `Checker` may issue reads `Diff` may not, because `doctor` never feeds a plan and never writes state. It is not a back door into `plan`: the engine calls optional capabilities only from the verbs that declare them.

The plan/apply engine is target-agnostic and consumes exactly what `kastor validate` assembles (loaded module, dependency graph, topological order) plus the state file — the same shape as the codegen engine's `Generate(job)` contract.
 
---
 
## 7. Deferred to v1+
 
- **Memory/state config** on agents (conversation memory, vector stores)
- **Guardrails** blocks (input/output filters, budgets, rate limits). Tool
  approval (§3.2) is **not** a precedent for these, and the line between them is
  stated rather than felt: *kastor configures what a target natively models as
  an attribute of the resource kastor creates; it does not implement
  enforcement.* Approval passes that test — `claude_agents` and `eve` each have
  a per-tool approval attribute, and kastor was already authoring one on every
  agent whether or not the spec mentioned it (leaving it unset is what deployed
  agents that applied green and could call nothing). Making it visible added no
  capability; it surfaced one kastor was exercising silently. Budgets, rate
  limits, and content filters are weighed against the same test when they are
  asked for, and admitting one of them requires passing it — not citing this
  entry.
- Multi-agent orchestration graphs (routing, handoffs) beyond simple references
- Module registry / package management for sharing `.agent`/`.tool` files
- **More credential resolvers** — `vault://`, `op://`, `aws-sm://` alongside v0's `env://` and `connection://` (§3.6). Purely additive: the scheme set is closed and rejects unknown schemes, so each new resolver is an addition within the language version. Each also brings a dependency and an auth story of its own, and would be the first scheme requiring kastor to *read* a secret — so each needs its own design pass. Kastor holding or brokering a credential is **not** on this list; it is a permanent non-goal (§3.6).
- **Per-environment variables** — staging vs. production values for a server's `url` and other spec attributes (the tfvars-shaped problem)
- Remote state backends
---
 
## 8. v0 Milestones
 
1. Parser + `kastor validate` for `.agent`, `.tool`, `.prompt`, project file
2. `kastor build` with **two** codegen targets: LangGraph and eve
3. `kastor plan/apply` with **one** platform provider: Claude Managed Agents
4. Examples repo: the weather agent end-to-end on both paths
Two codegen targets + one provider prove the hybrid thesis. Everything else is expansion.

## 9. Consumers & design constraints

Kastor has two consumer classes, in deliberate sequence:

1. **Humans (v0–v1).** Developers hand-writing specs, reading diagnostics,
   reviewing diffs in PRs. Human ergonomics — readable HCL, `kastor fmt`, clear
   error text, this document — are first-class permanently. Nothing in the
   AI-consumer direction may regress the human path.

2. **AI agents as the primary consumer (v1+).** The long-term thesis: people
   build personalized agents *by instructing an AI*, and that AI expresses the
   result as a Kastor module — validated, versioned, diffable, reviewable by a
   human. Kastor is the stable, typed substrate that makes AI-built agents
   trustworthy rather than ad hoc.

Every design decision is evaluated against both consumers. Concretely, the
AI-consumer thesis imposes these standing constraints:

- **Diagnostics are machine-readable and self-repair-oriented.** Every error
  states what was found, what was expected, and where (file:line + block
  address). A structured output mode (`--json`) is required, not polish:
  validation errors are the AI's self-correction loop.
- **The language is versioned.** Modules may declare the spec version they
  target; parsers reject versions they don't understand rather than
  misinterpreting them. Syntax changes are additive within a version.
- **Documentation is generated, not hand-maintained.** The schema structs are
  the source of truth; reference docs for the syntax derive from them, so an
  AI reading the docs and the parser enforcing the language can never
  disagree.
- **Determinism everywhere** (already a project convention): same input, same
  output — byte-identical builds, stable ordering, reproducible plans. AI
  workflows compound nondeterminism; the toolchain must contribute none.
- **The toolchain itself is agent-operable.** The pipeline behind the CLI is
  equally exposable via an MCP server (`validate`/`build`/`plan`/`apply` plus
  schema/introspection tools), so an AI can drive the full lifecycle without
  shelling out or screen-scraping text output.

Sequencing: these constraints shape decisions from now on, but implementation
lands after the core thesis is proven (§8 — two codegen targets, one platform
provider). The MCP server and generated docs are v1 milestones, not v0 scope.
