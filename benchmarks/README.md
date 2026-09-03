# Benchmark corpus

Each directory under `cases/` is one self-contained evaluation case:

```text
cases/<case-id>/
  case.toml           what to explain, and what to expect
  understanding.json  the curated understanding model (optional)
  repo/001-<name>/    a snapshot of the repository, committed in order
  repo/002-<name>/
```

`codewalk eval benchmark cases/<case-id>` materialises the snapshots into a
throwaway git repository (one commit per snapshot, tagged `snapshot-001`,
`snapshot-002`, …), generates a walkthrough for the range the case declares, and
evaluates it.

## Why understanding models instead of reference walkthroughs

Many different explanations of the same change can be equally good, so
benchmarking against one canonical piece of prose would measure similarity
rather than quality. `understanding.json` instead states *what a reader has to
come away knowing*. A judge checks whether each item was conveyed, in whatever
words the walkthrough chose.

Items are also weighted. `incidental` entries are things a good walkthrough may
reasonably ignore; spending significant attention on them counts against
selectivity rather than for coverage.

## Adding a case

1. Create the directory and write the snapshots. Keep them small: a case exists
   to test one kind of understanding, not to be a realistic application.
2. Write `case.toml`, declaring `base` and `head` as snapshot tags.
3. Write `understanding.json` by reading the fixture yourself and recording what
   you would need a colleague to know. Do not generate it from a walkthrough —
   that would make the benchmark grade the pipeline against itself.
4. Run `codewalk eval benchmark cases/<case-id> --mode smoke` to check it loads.

## Publishing rules

Everything here is published. Cases must use synthetic repositories with
fictional systems, `example.com` domains and generic paths — never private code,
proprietary excerpts or anything identifying a person or organisation.

## Current cases

| Case | Kind | What it tests |
| --- | --- | --- |
| `01-config-timeout` | change | Depth calibration on a trivial change: does the walkthrough stay short? |
| `02-async-order-completion` | change | An architectural change: a synchronous path becomes asynchronous, with new state and a new process. |
| `03-checkout-codebase` | codebase | Whole-repository explanation of a two-process service. |
