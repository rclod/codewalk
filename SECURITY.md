# Security policy

## Reporting a vulnerability

Please report security issues privately through GitHub's private vulnerability
reporting on this repository ("Security" → "Report a vulnerability") rather than
in a public issue.

Include what you can: affected version, reproduction steps, and what an attacker
could achieve. We will acknowledge the report, keep you updated while we
investigate, and credit you in the fix unless you prefer otherwise.

## Scope and threat model

codewalk is a local developer tool that reads repositories and sends selected
context to a model provider you configure. The properties below are the ones we
consider security-relevant.

### Repository integrity

codewalk is observational. It runs `git` through an allowlist of read-only
subcommands, and its file tools resolve every path inside the repository root,
including through symlinks. A way to make codewalk modify, delete or exfiltrate
files outside the analysed repository is a vulnerability.

### Prompt injection

Repository content — source, comments, Markdown, commit messages, fixtures,
strings — is untrusted data. Tool results are wrapped in explicit markers and
every stage is instructed that content inside them is material to explain, never
instruction to follow.

A repository that can make codewalk ignore its constraints, read outside the
sandbox, exfiltrate credentials, or take actions on the user's behalf is a
vulnerability. Please report it with the smallest reproducing fixture you can.

### Credentials

API keys are read from environment variables at use time. They are never written
to configuration, run records, logs, walkthroughs, evaluation artifacts or error
messages; provider errors are scrubbed before they surface. A path that leaks a
key is a vulnerability.

### The local HTTP service

`codewalk serve` binds loopback and is unauthenticated by design. It rejects
cross-origin browser requests as a DNS-rebinding defence, and refuses to bind a
non-loopback address without `--allow-remote`.

Running with `--allow-remote` exposes an unauthenticated API that can read the
repository, and is outside the supported threat model — do not do it on an
untrusted network. Bypassing the loopback default or the origin check without
that flag is a vulnerability.

### Agent harnesses

Harnesses are invoked in read-only postures (Claude Code in plan mode, Codex
with a read-only sandbox), and the ACP client declines permission requests for
mutating tool calls. A harness invocation that permits repository modification
without the user asking for it is a vulnerability.

### Out of scope

- The behaviour of third-party model providers and harnesses themselves.
- Anything that requires an attacker to already control your machine, your shell
  environment or your codewalk configuration.
- Inaccurate walkthroughs. Explanation quality is a product concern, handled
  through the evaluation system — please report those as ordinary issues.

## Supported versions

Fixes land on the latest release. There is no long-term support branch yet.
