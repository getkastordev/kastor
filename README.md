# Kastor

**Kastor is a source-of-truth layer for AI agents.**

Define agents, tools, prompts, models, and plugin-backed targets in HCL. Validate the spec. Compile it to runnable framework code, or reconcile hosted agents with Terraform-style `plan` / `apply` / `state`.

```sh
kastor init examples/weather
kastor validate examples/weather
kastor build examples/weather
kastor plan examples/weather
```

Agents today are often split across framework code, prompt files, tool files, platform UI settings, and environment configuration. Kastor's idea is that agents need a versionable, reviewable, declarative contract before they become serious software.

The full design lives in [SPEC.md](SPEC.md).

## Status

Kastor is an early proof of concept.

Working today:

- scaffold a new module with `kastor new`
- install checksum-verified plugins with `kastor init` and a committed lock file
- parse `.agent`, `.tool`, `.prompt`, and `kastor.hcl`
- validate references and prompt variables
- declare versioned target plugins separately from target instances
- build runnable LangGraph and eve projects
- require human approval for selected tools, portably across LangGraph, eve, and Claude Managed Agents
- run `kastor plan` / `kastor apply` / `kastor destroy` against the built-in in-memory platform
- reconcile hosted [Claude Managed Agents](#quickstart-hosted-claude-agents) through the Anthropic target plugin
- local state file, three-way diffs, and drift detection
- [VS Code syntax highlighting and file icons](#vs-code-support)
- examples: [weather agent](examples/weather), [content scheduler](examples/scheduler), [support triage](examples/support-triage)

Planned for v0:

- structured `--json` rendering for diagnostics and plans

Kastor is **not** an agent runtime.

## Demo

![Kastor building the agent from files](./docs/assets/demo-1.gif)

## How it works

```text
.agent + .tool + .prompt + kastor.hcl
                │
                ▼
        kastor validate
                │
      ┌─────────┴─────────┐
      ▼                   ▼
kastor build        kastor plan/apply
framework code      hosted agents
(LangGraph, eve)    (Claude Managed Agents)
```

Kastor has two paths. Targets choose an implementation explicitly through
`kastor.required_plugins`; their labels remain ordinary module-local instance
names:

- `kastor build` compiles a Kastor module into runnable framework code.
- `kastor plan` / `kastor apply` reconciles long-lived hosted agents with state, diffs, and drift detection.

## Example

An agent in Kastor is a small declarative spec:

```hcl
agent "weather" {
  description = "Answers weather questions for a location and date"

  model         = model.fast
  system_prompt = prompt.weather_system
  tools             = [tool.web_search]
  requires_approval = [tool.web_search]

  input "location" {
    type        = string
    description = "The location to get weather for"
  }

  input "date" {
    type     = string
    optional = true
  }

  output "weather" {
    type = string
  }
}
```

The generated code is not the source of truth. The Kastor module is.

`tools` is the grant: omission means the agent cannot call a tool.
`requires_approval` narrows that grant, so the named tools pause for a human
while the rest run unsupervised.

## Quickstart: start your own module

`kastor new` installs the LangGraph plugin, locks its exact release, and asks
the plugin for a minimal working module. Framework templates live with their
plugins instead of in the Kastor binary:

```sh
kastor new demo
cd demo
kastor validate
kastor build
```

The scaffolded agent answers a question by fetching web pages through the reference MCP fetch server (run via [`uvx`](https://docs.astral.sh/uv/), no API key needed). The scaffold's `README.md` walks through running the generated project end to end.

Choose another plugin-owned starter with `--from`, for example
`kastor new --from github.com/getkastordev/kastor-eve demo`. `new` refuses a
directory that already contains visible files; `--force` overwrites only the
scaffold's own file names and keeps everything else.

## Quickstart: no credentials required

This path validates the example and runs `plan` / `apply` against the built-in in-memory platform target. It does not create remote resources and does not require API keys.

```sh
go build -o kastor ./cmd/kastor
./kastor init examples/weather/
./kastor validate examples/weather/
./kastor plan examples/weather/
./kastor apply examples/weather/
```

Example plan output:

```console
$ kastor plan examples/weather/
  + agent.forecast (not in state)
  + agent.geocoder (not in state)
  + agent.weather (not in state)

Plan for target.memory: 3 to create, 0 to update, 0 to delete, 0 unchanged.
```

`kastor plan` is a pure read: it never touches remote resources or the state file, and it needs no network beyond the platform it is planning against. Updates show attribute-level diffs, and out-of-band remote changes surface as drift warnings.

## Readiness: `kastor doctor`

`plan` and `apply` answer "does the remote match the spec". They cannot answer
"can the thing that is deployed actually run" — an agent whose MCP connections
are unauthenticated and whose tool permissions deny everything matches its spec
exactly, plans clean, and cannot serve a request. That question has its own
verb:

```console
$ kastor doctor --target claude_agents examples/hubspot/
Environment:
  ✓ ANTHROPIC_API_KEY: environment variable is set
      target.claude_agents authenticates against this platform

agent.sales (agent_011CZq…)
  ✓ agent_011CZq…: remote object exists
  ✗ cred_011CZkZDLs7fYzm1hXNPeRjv ("HubSpot Prod"): connection is not authenticated: the OAuth grant has expired
      mcp_server.hubspot references connection://cred_011CZkZDLs7fYzm1hXNPeRjv, whose grant
      expired at 2026-08-01T09:14:22Z and carries no refresh token; re-authorize the
      connection on the platform
  ✓ search: tool is granted with permission "always_allow"

Readiness for target.claude_agents: 3 ok, 1 failed, 0 could not be verified.
```

`doctor` is read-only: it never invokes an agent, never changes a remote object,
and never writes state. It exits 0 when everything is ready and 1 when anything
is not.

Three things worth knowing:

- **"Could not verify" is not "missing."** A check reports `ok`, `failed`, or
  `unknown`, and the third is load-bearing. An unreachable vault reports `?
  could not verify the credential against the vault`; a vault that answers and
  holds no such credential reports `✗ credential does not exist in the vault`.
  Those are different problems with different fixes, so they are never
  collapsed. `unknown` still counts against readiness — the command did not
  establish that the module is ready.
- **Credential ids print with their display name alongside.** The id is the
  identifier because a display name is nullable and non-unique on the platform,
  but `cred_011CZ… ("HubSpot Prod")` is what you can act on.
- **Environment readiness needs no platform at all.** The `env://` refs your
  module declares are compared against your shell, so `doctor` answers "what does
  this module need from my environment before it will run" offline.

## Quickstart: hosted Claude agents

This is the hosted path: a platform target selecting the Anthropic plugin
reconciles agents in your Anthropic organization through Claude Managed Agents.
Unlike the memory plugin, `apply`
here creates real remote objects, and `destroy` **archives them irreversibly** —
read [Destroying a Claude agent](#destroying-a-claude-agent) before you run it.

Prerequisites:

- an Anthropic API key with access to Managed Agents
- an endpoint URL for every MCP server the module's `mcp` tools name

Write a module — one project file, one agent, two tools, one prompt:

```hcl
# kastor.hcl
kastor {
  required_plugins {
    anthropic = {
      source  = "github.com/getkastordev/kastor-anthropic"
      version = "~> 0.1"
    }
  }
}

model "haiku" {
  provider = "anthropic"
  id       = "claude-haiku-4-5"
}

target "claude_agents" {
	type   = "platform"
	plugin = "anthropic"

	config {
		api_key_env = "ANTHROPIC_API_KEY"
		vault_id    = "vlt_011CZkZDLs7fYzm1hXNPeRjv"
	}
}

# The MCP server tool.tavily_search binds to. Declaring it is what makes
# mcp://search-server/<tool> resolvable. `ref` names *where* the credential
# lives — never the credential: on this target it is one Anthropic already
# holds, in the vault the target names.
mcp_server "search-server" {
  url = "https://mcp.tavily.com/mcp"

  auth {
    ref = "connection://cred_011CZkZDLs7fYzm1hXNPeRjv"
  }
}
```

```hcl
# researcher.agent
agent "researcher" {
  description = "Answers research questions with web search and a hosted MCP server"

  model         = model.haiku
  system_prompt = prompt.researcher_system

  tools = [tool.web_search, tool.tavily_search]
}
```

```hcl
# research.tool
tool "web_search" {
  description = "Claude's hosted web search"

  returns {
    type = string
  }

  source {
    kind = "builtin"
  }
}

tool "tavily_search" {
  description = "Search the web through Tavily's hosted MCP server"

  param "query" {
    type = string
  }

  returns {
    type = string
  }

  source {
    kind = "mcp"
    uri  = "mcp://search-server/tavily_search"
  }
}
```

```text
# researcher_system.prompt
---
name     = "researcher_system"
requires = []
---
You are a research assistant. Answer concisely and cite your sources.
```

One environment variable — the platform credential:

```sh
export ANTHROPIC_API_KEY=sk-ant-YOUR-KEY
```

`ANTHROPIC_API_KEY` is the default credential; the `auth` block above only names
it explicitly. Any other variable works — `api_key_env = "ANTHROPIC_API_KEY_PROD"`
— and the `auth` block may be omitted entirely.

The MCP server needs nothing further in your shell. Its **address** is spec — the
`mcp_server` block — and its **credential** is one Anthropic already holds, named
by a `connection://` ref and resolved by the platform, not by kastor. Kastor is
never the credential holder: it stores no token, refreshes nothing, and sends the
platform the server's name and URL only. State records the reference, never the
value, so rotating the secret behind it is invisible to kastor — correct, because
kastor does not manage the secret.

`plan` and `apply` never contact the vault. Whether a credential actually
resolves is a readiness question, not a pending-change one, so it belongs to
[`kastor doctor`](#readiness-kastor-doctor) — which reports a typo'd, archived, or
misdirected credential by name.

Plan, then apply:

```console
$ kastor plan
  + agent.researcher (not in state)

Plan for target.claude_agents: 1 to create, 0 to update, 0 to delete, 0 unchanged.

$ kastor apply
  + agent.researcher (not in state)

Plan for target.claude_agents: 1 to create, 0 to update, 0 to delete, 0 unchanged.

Applied target.claude_agents: 1 created, 0 updated, 0 deleted.
```

Kastor writes the remote id to `kastor.state.json` and stamps
`metadata.kastor_managed = "agent.researcher"` on the remote agent. That marker is
an ownership assertion: kastor refuses to compare — and therefore to update — a
remote agent that does not carry it, so an agent someone created in the Console
can never be silently overwritten by an apply.

Edit the spec and re-apply, and the change lands as an attribute-level update.
Change the agent in the Console instead, and the next `plan` reports drift and
plans the update that converges it back to the spec.

### What `claude_agents` does not support

The Managed Agents resource is narrower than the Kastor agent block, and Kastor
treats fields that are meaningless for a target as errors rather than ignoring
them (SPEC.md §3.5). On this target:

| Spec | Result |
| --- | --- |
| `source` kind `http`, `script`, or `runtime` | Error. These are client-executed tools, and kastor is not a runtime. Wrap the implementation in an MCP server and declare it `kind = "mcp"`. |
| `source` kind `builtin` outside the hosted toolset | Error. The hosted set is `bash`, `edit`, `glob`, `grep`, `read`, `web_fetch`, `web_search`, `write`. |
| `model` with `provider` other than `"anthropic"` | Error. |
| `params { temperature = ... }`, `max_tokens`, anything but `speed` | Error. The platform's model object exposes `id` and `speed` only, so `speed` is the one param that maps; omitted, it is `standard`. |
| `input` / `output` blocks | Sent nowhere. Managed Agents has no IO-contract field; the blocks stay valid spec and still drive references and validation, but they are not part of the remote object and never appear in a diff. |

These are **plan-time** errors, not validation errors — see the caveat below.

### Destroying a Claude agent

`kastor destroy` deletes the state entry and **archives** the remote agent.
Archive is irreversible on this platform: the agent cannot be restored, and it
stays listed in the Anthropic Console permanently. A later `kastor apply` does
not resurrect it — it creates a new agent with a new id.

`destroy` reads before archiving, so an agent that is already gone or already
archived is a no-op, and an archived agent reads as absent — which is why a plan
after destroy proposes a create rather than an update.

`destroy` does not prompt for confirmation in v0. On this target, run
`kastor plan` first and read the `-` lines.

### One caveat: these errors arrive at plan, not validate

`kastor validate` is target-agnostic — it parses, resolves references, and checks
prompt variables, and it knows nothing about any provider. The table above is
enforced by the provider, when it renders an agent for the platform.

That rendering happens during `plan`, for every resource — including one kastor
has not created yet, where there is no remote object to compare against. So a
module that could never apply fails the plan, naming the resource and the target:

```console
$ kastor validate
Success! Module is valid: 1 agent, 1 tool, 1 prompt, 1 model, 1 target.

$ kastor plan
kastor: agent.probe: cannot be created on target.claude_agents: tool.rest: source kind "http" cannot be mapped to Claude Managed Agents; custom tools are client-executed and kastor is not a runtime; use an MCP-server wrapper with source kind "mcp"

$ echo $?
1
```

A clean plan therefore does mean "this module maps onto this target". What it
still cannot promise is that the platform will accept it — credentials, quotas,
and model availability are only known to the API. When one of those fails, apply
stops at the first failure and state records everything applied before it, so a
re-run plans exactly the remainder.

## Quickstart: generate and run LangGraph

This path compiles the weather agent to a runnable LangGraph project.

Prerequisites:

- Go 1.26+
- Python 3.11+
- an OpenAI API key
- a [Tavily](https://tavily.com) API key, because the example's search tool runs against Tavily's hosted MCP server

Compile the spec to a LangGraph project:

```sh
go build -o kastor ./cmd/kastor
./kastor validate examples/weather/
./kastor build examples/weather/
```

`kastor build` writes the generated project to `examples/weather/gen/langgraph` — the target's declared `output`.

Generated output is not committed. It is reproducible from the spec, and codegen determinism is enforced by tests.

Set up the generated project:

```sh
cd examples/weather/gen/langgraph
python3 -m venv .venv
. .venv/bin/activate
pip install -r requirements.txt
```

The example's `web_search` tool is pinned to an MCP server and tool by its spec URI:

```text
mcp://search-server/tavily_search
```

The server it names is declared in `examples/weather/kastor.hcl`, which is what makes that URI resolvable:

```hcl
mcp_server "search-server" {
  url = "https://mcp.tavily.com/mcp"

  auth {
    ref = "env://TAVILY_API_KEY"
  }
}
```

`kastor build` turns that block into `gen/langgraph/mcp_servers.json` — generated output like everything else in the directory, rewritten by the next build. There is nothing to write by hand. (`KASTOR_MCP_CONFIG` still overrides it wholesale for one run, a development escape hatch for aiming at a local server instance.)

The credential is **referenced, never held**: `env://TAVILY_API_KEY` names a variable, and the generated bridge reads it in your own process at call time. No token is written into the generated project, and none appears in `mcp_servers.json`.

The spec URI's last path segment, `tavily_search`, must name a tool the server actually advertises. If it does not, calls fail with `does not expose tool`.

Export the model credential and the server's. The example's `model "fast"` block uses provider `openai`:

```sh
export OPENAI_API_KEY=sk-...
export TAVILY_API_KEY=tvly-...
```

Run the agent:

```sh
python3 main.py weather --inputs '{"location": "Lisbon", "date": "tomorrow"}'
```

It prints the agent's declared output contract as JSON:

```json
{
  "weather": "..."
}
```

The generated `README.md` inside `gen/langgraph` owns the run-the-project side in full: every agent's inputs and outputs, tool bindings, and MCP configuration.

One v0 caveat: `agent.weather`'s optional `forecast_context` input references `agent.forecast`'s output. That reference is validated at compile time and orders the dependency graph, but generated code does not run the upstream agent for you. If you want the context, run `forecast` yourself and pass its summary via `--inputs`.

## File types

A Kastor module is a directory tree containing declarative files:

| File type | Purpose |
| --- | --- |
| `.agent` | Agent definitions: model, prompt, tools, inputs, outputs, dependencies |
| `.tool` | Tool interface plus implementation source |
| `.prompt` | Prompt template plus required variables |
| `kastor.hcl` / `*.kastor` | Project file: models, targets, defaults |

References connect blocks by address, not by file path. For example, an agent references `model.fast`, `prompt.weather_system`, and `tool.web_search`.

References also build the dependency graph. A reference like `agent.forecast.output.summary` validates that the output exists and orders the graph.

## VS Code support

The [`extensions/vscode`](extensions/vscode) extension adds syntax highlighting
and file icons for every Kastor file type. Highlighting only — no language
server, no commands, no settings.

Until it is on the Marketplace, install it from source:

```sh
cd extensions/vscode
npm install
npm run package
code --install-extension kastor-0.1.0.vsix
```

Open any Kastor file and it activates. The grammar uses HashiCorp's TextMate
scope names, so Kastor picks up your theme's Terraform colors, with extra rules
for the constructs that are Kastor's own: block references, the bare type
keywords, the `source` kind and `target` type enums, and `{{variable}}` prompt
templates.

Two details worth knowing:

- HCL that Kastor rejects — `${...}` interpolation, heredocs, function calls,
  `for` expressions, ternaries — is deliberately left uncolored, because
  highlighting it would suggest it works.
- Icons ship as VS Code *language icons*, which appear under Seti, the default
  file icon theme. VS Code offers no way to add icons to a third-party icon
  theme, so under Material Icon Theme or vscode-icons, Kastor files keep that
  theme's generic icon.

The extension's [README](extensions/vscode/README.md) covers development and
publishing.

## What Kastor is not

Kastor is not an agent runtime.

Frameworks like LangGraph still execute agents. Hosted platforms like Dify still run managed agents. Kastor sits above them as the declarative source-of-truth layer: model, prompts, tools, inputs, outputs, dependencies, and targets.

Kastor also does not try to standardize the full behavior or control loop of an agent. That layer is still changing quickly. The narrower bet is that the outer contract around agents should be reviewable, versionable, and diffable.

## Why not Terraform?

Terraform is great for managing remote resources. A Terraform provider for hosted agents may make sense later.

Kastor starts one layer earlier: the agent spec itself.

The same Kastor module should be able to:

- generate runnable framework code with `kastor build`
- reconcile hosted platform agents with `kastor plan` / `kastor apply`

That codegen path is why Kastor is a separate toolchain rather than only a Terraform module or provider.

## Why not just LangGraph?

LangGraph is a runtime/framework. Kastor is not trying to replace it.

Kastor defines the agent contract and generates a LangGraph project from that spec. The generated code is an output; the Kastor module is the source of truth.

## Install

The canonical repository is now `getkastordev/kastor`. The new `go install`
path still requires a release declaring the new module path; the GitHub
transfer alone does not make that installation available. See [RELEASING.md](RELEASING.md)
for release order. The Homebrew tap has also moved to `getkastordev/tap`.

Homebrew (macOS):

```sh
brew install getkastordev/tap/kastor
```

Install script:

```sh
curl -fsSL https://raw.githubusercontent.com/getkastordev/kastor/main/scripts/install.sh | sh
```

The install script verifies the release checksum, installs to `/usr/local/bin` or `~/.local/bin`, and never uses `sudo`.

With Go 1.26.4+ (after the new-path release):

```sh
go install github.com/getkastordev/kastor/cmd/kastor@latest
```

Or download an archive for your platform from the [releases page](https://github.com/getkastordev/kastor/releases), verify it against `checksums.txt`, and put the `kastor` binary on your PATH.

### Install target plugins

Declare plugins in `kastor.required_plugins`, then initialize the module:

```sh
kastor init
git add .kastor.lock.hcl
```

`init` resolves matching GitHub releases, downloads the current platform
archive, verifies the publisher's `checksums.txt`, installs it in the user
cache, and writes the exact version, protocol, platform assets, and checksums
to `.kastor.lock.hcl`. Commit that file. Normal commands never fetch or change
dependencies.

For automation, use `kastor init --frozen` to reject lock drift. Add
`--offline` to prove the verified cache is sufficient, or use
`KASTOR_PLUGIN_CACHE_DIR`/`--plugin-cache` for an explicit CI cache location.
Use `kastor init --upgrade` only when intentionally refreshing selections.

Execution discovery order is:

1. `KASTOR_PLUGIN_<LOCAL_NAME>` — an exact executable path, such as
   `KASTOR_PLUGIN_LANGGRAPH=/work/kastor-langgraph`.
2. `KASTOR_PLUGIN_DIR` — a directory containing source-named executables.
3. The lock-selected cached executable, verified against its release archive.
4. `PATH`, only for an uninitialized module with no lock (migration fallback).

The first two are explicit development overrides and never mutate the module's
requirements or lock. When a lock exists, a missing or tampered cached binary
is an error with a `kastor init` repair instruction; it is never silently
replaced by an unrelated `PATH` executable.

The core performs a protocol, source-identity, target-kind, and version
handshake before sending the canonical module IR. Plugin stdout is reserved
for protocol traffic; diagnostics and logs belong on stderr.

## Development

```sh
go build ./...   # build everything
go test ./...    # run all tests
go vet ./...     # static checks
gofmt -l .       # formatting check
```

SPEC.md is the source of truth for design decisions. CLAUDE.md documents the day-to-day development conventions.

## Early feedback

I'm currently looking for feedback from people building agents in production or experimenting with agent tooling.

Useful feedback areas:

- whether the source-of-truth layer makes sense
- where the spec is too rigid or too loose
- what framework or hosted platform target should come next
- what would make the first-run experience smoother

To follow or discuss the project:

- star/watch the repo for updates
- open an issue for bugs or design feedback
- join the early Discord: [invite](https://discord.gg/bb4UwtJFe)
