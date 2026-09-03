# Contributing to codewalk

Thanks for considering a contribution. This document covers how to build and
test the project, and the few standards that are specific to it.

## Development setup

You need Go 1.22+ and `git`. Nothing else — no services, no database, no
external accounts.

```bash
git clone https://github.com/rclod/codewalk.git
cd codewalk
make build          # ./bin/codewalk
make test           # full test suite
make check          # vet, format check and tests
```

Running codewalk against a real repository needs a model backend: either a
provider API key (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `XAI_API_KEY`) or a
local agent harness. `codewalk config check` reports what is usable. The test
suite needs neither: model behaviour is covered by evaluation, not by unit
tests.

## Project shape

Read [`DESIGN.md`](DESIGN.md) first. The short version:

- `internal/walkthrough` holds the canonical schema. Every surface consumes it.
- `internal/pipeline` holds the stages and the prompts.
- `internal/tools` and `internal/gitrepo` are the only paths to a repository,
  and both are read-only by construction.
- `internal/eval` measures walkthrough quality.

## Standards specific to this project

**Everything here is published.** Fixtures, examples, documentation and
benchmark cases must use fictional systems, `example.com` domains and generic
paths such as `/home/user/projects/example-app`. Never contribute private code,
proprietary excerpts, real hostnames, personal paths, real names or anything
resembling a credential. Placeholders should be obviously synthetic
(`OPENAI_API_KEY=your-api-key`).

**codewalk explains code; it does not grade it.** This boundary erodes easily.
If you are changing a prompt, a schema field or an evaluation dimension, check
that it does not nudge the system toward reporting problems, recommending
changes or judging quality. The neutrality dimension and the review-drift
detector exist to defend this, and they are worth extending.

**Nothing may modify a repository under analysis.** New git usage goes through
the allowlist in `internal/gitrepo`. New file access goes through the path
sandbox in `internal/tools`. If you find yourself wanting to bypass either, that
is the discussion to have in the issue first.

**Repository content is untrusted.** Anything read from a repository is data to
explain, never instruction. Tool results are wrapped in explicit markers; keep
new tools consistent with that.

## Testing

Deterministic behaviour is tested conventionally and thoroughly: change
detection, base and head selection, configuration precedence, schema handling,
serialisation, the path sandbox, provider adapters, the tool loop, HTTP
contracts, reference validation, diagram validation, run persistence, CLI
parsing.

Model behaviour is **not** tested with snapshots of generated prose. Those tests
break on every prompt change without telling you whether quality moved. Use the
evaluation system instead:

```bash
codewalk eval check latest
codewalk eval benchmark benchmarks/cases/01-config-timeout --mode smoke
```

If you change an evaluator, add a degraded variant in
`internal/eval/degrade.go` and assert that the dimensions it should move
actually move. An evaluation system that cannot detect a known defect is worse
than none.

## Changing prompts

Prompts live in `internal/pipeline/prompts.go` and `internal/eval/prompts.go`,
and are versioned. Bump the version constant whenever text changes in a way that
could alter output — run records store the version, and attribution depends on
it.

Prompt changes should be evaluated, not just eyeballed:

```bash
codewalk eval suite benchmarks/cases --mode standard
codewalk eval compare <before-run> <after-run>
```

## Adding a benchmark case

See [`benchmarks/README.md`](benchmarks/README.md). Prefer a small number of
well-understood cases over many poorly understood ones. Write the understanding
model by reading the fixture yourself; deriving it from a generated walkthrough
would make the benchmark grade the pipeline against itself.

## Pull requests

- Keep the change focused, and say what you verified.
- Run `make check` before pushing.
- Update `DESIGN.md` when you change architecture, the schema, or a tradeoff.
- New behaviour needs tests, or a note explaining why evaluation covers it
  instead.

## Code style

Standard Go style, `gofmt`-formatted. Comments explain *why*, not what the code
plainly says; the existing code aims for that, so match it. Prefer the standard
library — the project has one direct dependency and intends to keep the list
very short.

## Reporting bugs and proposing features

Use the issue templates. For walkthrough quality problems, the most useful
report includes the walkthrough JSON (with anything private removed), what you
expected to understand and did not, and the backend you used.

## Code of conduct

Participation is governed by [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md).
