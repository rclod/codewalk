# codewalk

**Guided code walkthroughs.** codewalk explains code so you can understand it
quickly: what a change does, how a system fits together, and where to look next.

It is not a code review tool, not a documentation generator, and not a
summarised `git diff`. It behaves like an experienced engineer sitting beside
you, walking you through unfamiliar code.

```bash
codewalk pr                    # explain the current branch, or your uncommitted work
codewalk codebase              # explain the whole repository
codewalk ask latest "why is the worker involved?"
codewalk serve                 # read it in a local web UI
```

## What it does

**Change walkthroughs** explain the behavioural and architectural meaning of a
change — the working tree, staged changes, a branch, a commit or a range. Not
"`foo.go` adds a method and `bar.go` calls it", but what behaviour existed
before, what exists now, which components participate, what state changes, and
which parts of the diff you can ignore.

**Codebase walkthroughs** explain what a repository is, how it runs, its major
subsystems, its important execution paths, where state lives, and what it
integrates with — discovered from the code, not from the directory names.

**Follow-up questions** reuse everything the walkthrough already established, so
"go deeper on step 4" or "how did this work before?" costs one question, not a
second investigation.

### The design goal

> **Mental Model Efficiency**: how completely and accurately you understand the
> important behaviour and architecture, per unit of attention you spend.
>
> Nothing important missing. Nothing important wrong. Almost nothing
> unnecessary.

A walkthrough adapts its depth to how much context the change actually requires.
A configuration change gets a few sentences. A synchronous path becoming
asynchronous gets before/after, a flow diagram and the components involved.

### What it deliberately does not do

codewalk explains code; it does not grade it. It will not tell you about bugs,
vulnerabilities, performance problems, style, missing tests or refactoring
opportunities. It reads that code — tests, validation, authentication,
concurrency — but only to explain how the system behaves.

> Not this: "This creates an N+1 query and should be optimised."
>
> This: "This loop loads each record independently, so one database lookup
> happens per item."

If you want critique, ask for it in a follow-up. The default stays explanatory.

## Install

From a clone:

```bash
make install     # builds and installs to $(go env GOPATH)/bin
make build       # builds ./bin/codewalk instead
make test        # runs the test suite
```

Once the repository is published, installation is a single command:

```bash
go install github.com/rclod/codewalk/cmd/codewalk@latest
```

Either way you get one static binary, with the web UI and its assets embedded.
Make sure your Go bin directory is on `PATH`:

```bash
export PATH="$PATH:$(go env GOPATH)/bin"
codewalk --help
```

To upgrade, pull and re-run `make install`. To uninstall, run `make uninstall`
and, if you want the stored walkthroughs gone too, delete `~/.codewalk`.

Requires Go 1.22+ and `git` on `PATH`.

## First run

codewalk needs either a model provider API key or a local agent harness.

```bash
codewalk config init            # writes ~/.codewalk/config.toml
export ANTHROPIC_API_KEY=your-api-key
codewalk config check           # confirms what is usable
cd /home/user/projects/example-app
codewalk pr
```

Supported providers: **Anthropic**, **OpenAI**, and any **OpenAI-compatible**
endpoint (xAI, gateways, local servers).

Already have a coding agent installed? Use it instead of an API key:

```bash
codewalk pr --backend claude-code
codewalk pr --backend codex
codewalk pr --backend opencode
```

Harnesses are run in a read-only posture, and codewalk relies on the filesystem,
search and git access they already have.

## Using it

### Explaining a change

```bash
codewalk pr                          # branch vs base, else uncommitted work
codewalk pr --base main              # compare against a specific base
codewalk pr main..feature            # an explicit range
codewalk pr --staged                 # staged changes only
codewalk pr abc1234                  # a single commit
codewalk pr --focus "the database changes"
codewalk pr --depth brief            # or standard, deep; default adapts
```

### Output formats

```bash
codewalk pr                          # readable terminal output
codewalk pr --format markdown        # Markdown with Mermaid diagrams
codewalk pr --format json > walkthrough.json
```

`--format json` emits the canonical walkthrough on stdout and progress on
stderr, so redirection works. Terminal output hides deep dives by default; add
`--deep-dives` and `--diagrams` to see everything.

### Asking follow-ups

```bash
codewalk ask latest "why is the worker involved?"
codewalk ask latest "go deeper on step 3"
codewalk ask latest "where is this state persisted?"
codewalk ask latest "explain this assuming I don't know the billing architecture"
codewalk ask walkthrough.json "walk me through the frontend"
```

### The web UI

```bash
codewalk serve
```

One step at a time with progress and keyboard navigation, rendered diagrams,
collapsed deep dives, code references that open the source beside the
explanation, and a chat panel for follow-ups. It binds to loopback and is
unauthenticated by design; binding elsewhere requires `--allow-remote`.

### Browsing past walkthroughs

```bash
codewalk runs
codewalk show latest --format markdown
codewalk show latest --artifact evidence     # what the investigator found
```

## Configuration

Precedence, highest first:

```text
CLI flags -> environment variables -> .codewalk.toml -> ~/.codewalk/config.toml -> defaults
```

You should not need any of it to start. When you do:

```toml
[defaults]
backend = "anthropic"
depth = "auto"          # auto, brief, standard, deep
format = "text"

[analysis]
editor = true           # the clarity editor pass
grounding = true        # verify claims against the repository
git_history = true

# Different stages can run on different models. Optional; useful for
# experiments and for pairing a thorough investigator with a strong writer.
[agents.investigator]
backend = "openai"
reasoning_effort = "high"

[agents.author]
backend = "anthropic"
```

**API keys are never stored in configuration.** Each provider names the
environment variable that holds its key. `codewalk config show` tells you
whether a key is set, never what it is.

See [`docs/configuration.md`](docs/configuration.md) for every option.

## Safety and privacy

- **Your repository is never modified.** Git access goes through an allowlist of
  read-only subcommands; write commands are rejected by construction. Every file
  path resolves inside the repository root, symlinks included.
- **Repository content is treated as untrusted data.** Text in source, comments,
  Markdown or commit messages that looks like an instruction is material to
  explain, never a command to the agent.
- **No telemetry.** codewalk sends nothing anywhere except to the model provider
  you configure.
- **The provider trust boundary.** Generating a walkthrough sends selected
  repository context — diffs, file excerpts, search results — to your configured
  model provider or local harness. That is the one place your code leaves your
  machine. If your code cannot go to a third-party API, use a local harness or a
  self-hosted OpenAI-compatible endpoint.
- **Credentials** are read from the environment at use time and never stored,
  logged, persisted in run records, or included in error messages.

## Walkthrough quality

Walkthrough quality is a first-class capability, not an afterthought.

```bash
codewalk eval check latest                       # deterministic checks, cheap
codewalk eval run latest --mode standard         # add independent semantic judging
codewalk eval benchmark benchmarks/cases/02-async-order-completion
codewalk eval suite benchmarks/cases
codewalk eval compare run-a run-b                # blind pairwise comparison
```

Deterministic checks verify what tooling can answer — schema validity, whether
every referenced file, symbol and line range exists, whether diagrams parse,
whether the walkthrough drifted into code review. Semantic evaluation scores
grounding, essential coverage, mental model accuracy, selectivity, teaching
order, depth calibration, before/after clarity, navigability, concision,
neutrality and diagram utility, with structured observations rather than just
numbers.

Hard gates come before averages: a beautifully written walkthrough that invents
a component fails, however well it scores elsewhere.

See [`benchmarks/README.md`](benchmarks/README.md) and the evaluation section of
[`DESIGN.md`](DESIGN.md).

## How it works

```text
Repository / change
   -> Investigator      gathers evidence from the code
   -> Mental model      decides what you actually need to understand
   -> Planner           decides the teaching order and shape
   -> Author            writes the explanation
   -> Clarity editor    edits for comprehension only
   -> Grounding check   verifies claims against the repository
   -> Walkthrough
```

Stages are skipped when they cannot pay for themselves, run on independently
configurable backends, and are recorded with the prompt version and model that
produced them. Every generation is persisted as a run, so it can be re-read,
re-asked, re-evaluated and reproduced.

[`DESIGN.md`](DESIGN.md) explains the architecture, the schema and the
tradeoffs in full.

## Contributing

Contributions are welcome. Start with [`CONTRIBUTING.md`](CONTRIBUTING.md) and
[`DESIGN.md`](DESIGN.md). Everything in this repository is published, so
examples, fixtures and documentation must use synthetic systems and generic
paths.

## License

[MIT](LICENSE)
