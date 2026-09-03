# Configuration reference

codewalk works with no configuration at all: set one provider API key, or point
it at a local agent harness, and run `codewalk pr`. Everything below is optional.

## Precedence

```text
CLI flags
  -> environment variables (CODEWALK_*)
    -> repository config (.codewalk.toml)
      -> user config (~/.codewalk/config.toml)
        -> built-in defaults
```

Layers merge per key. A repository config can override one provider setting
without restating the rest of the entry.

## Files and locations

| Path | Purpose | Override |
| --- | --- | --- |
| `~/.codewalk/config.toml` | User configuration | `CODEWALK_CONFIG`, `CODEWALK_HOME` |
| `.codewalk.toml` or `.codewalk/config.toml` | Repository configuration | — |
| `~/.codewalk/runs/` | Persisted runs | `CODEWALK_RUNS_DIR` |

`codewalk config path` prints the resolved locations. `codewalk config init`
writes a starter file; `codewalk config show` prints the effective
configuration, with credentials reported as set or unset but never printed.

## Credentials

**API keys are never stored in configuration.** Each provider entry names the
environment variable that holds its key:

```bash
export ANTHROPIC_API_KEY=your-api-key
export OPENAI_API_KEY=your-api-key
export XAI_API_KEY=your-api-key
```

Keys are read at use time and never written to configuration, run records, logs,
walkthroughs or error messages. Run `codewalk config check` to see what is
currently usable.

## Sections

### `[defaults]`

| Key | Default | Meaning |
| --- | --- | --- |
| `backend` | `"anthropic"` | Backend for any stage that does not name its own |
| `model` | provider default | Model override for every stage |
| `base` | detected | Base branch for change walkthroughs |
| `depth` | `"auto"` | `auto`, `brief`, `standard` or `deep` |
| `format` | `"text"` | `text`, `markdown` or `json` |

`depth = "auto"` lets conceptual complexity decide, which is almost always what
you want: it is what keeps a one-line change from producing an architecture
report.

### `[analysis]`

| Key | Default | Meaning |
| --- | --- | --- |
| `editor` | `true` | Run the clarity editor stage |
| `grounding` | `true` | Verify claims against the repository |
| `git_history` | `true` | Allow agents to inspect commit history |
| `max_steps` | `40` | Tool-use iterations per stage |
| `max_changed_files` | `400` | Cap on changed files listed to agents |
| `max_diff_bytes_per_file` | `60000` | Per-file diff truncation |
| `max_diff_bytes_total` | `400000` | Whole-diff budget; omitted files are listed by name |
| `max_file_bytes` | `200000` | Per-read truncation |
| `include_generated` | `false` | Include lockfiles and generated output in diffs |

The editor and grounding stages cost extra model calls and materially improve
concision and accuracy. Turning both off is the fastest configuration; the CLI
equivalents are `--no-editor` and `--no-grounding`.

### `[server]`

| Key | Default | Meaning |
| --- | --- | --- |
| `host` | `"127.0.0.1"` | Bind address. Non-loopback requires `--allow-remote` |
| `port` | `7457` | Port |
| `open_browser` | `true` | Open a browser when `codewalk serve` starts |

### `[providers.<name>]`

```toml
[providers.anthropic]
type = "anthropic"              # anthropic | openai | openai_compatible
model = "claude-sonnet-5"
api_key_env = "ANTHROPIC_API_KEY"
max_tokens = 16000
reasoning_effort = "medium"     # low | medium | high, where supported
# base_url = "https://api.anthropic.com"
# request_timeout_seconds = 600
```

Use `type = "openai_compatible"` for xAI, gateways, self-hosted servers and
anything else speaking the OpenAI chat completions API:

```toml
[providers.local]
type = "openai_compatible"
model = "your-model"
api_key_env = "LOCAL_API_KEY"
base_url = "http://localhost:8000/v1"
```

### `[harnesses.<name>]`

A harness is a coding agent you already have installed. codewalk hands it a task
and reads the answer, relying on the filesystem, search and git access it
already has. Harnesses need no API key and are always run read-only.

```toml
[harnesses.claude-code]
type = "claude_code"            # claude_code | codex | opencode | acp | command
command = "claude"
# args = []                     # inserted before codewalk's own arguments
# model = "..."
# timeout_seconds = 900
# env = ["SOME_VARIABLE"]       # names forwarded from your environment
```

`type = "acp"` speaks the Agent Client Protocol over stdio and is experimental.
`type = "command"` runs any executable that reads a prompt on stdin and writes
an answer to stdout.

### `[agents.<role>]`

Roles: `investigator`, `mental_model`, `planner`, `author`, `editor`,
`grounding`, `followup`, `judge`, `extractor`.

```toml
[agents.investigator]
backend = "openai"
reasoning_effort = "high"
max_steps = 60

[agents.author]
backend = "anthropic"

[agents.editor]
enabled = false
```

You never have to configure this. It exists for pairing a thorough investigator
with a strong writer, and for evaluation experiments where exactly one variable
changes.

### `[eval]`

```toml
[eval]
mode = "smoke"                  # smoke | standard | full
judges = ["openai", "anthropic"]
corpus_dir = "benchmarks/cases"
```

Using judges from more than one model family reduces single-model bias.
Disagreements of 1.5 points or more are recorded rather than averaged away.

## Environment variables

| Variable | Effect |
| --- | --- |
| `CODEWALK_HOME` | codewalk's directory (default `~/.codewalk`) |
| `CODEWALK_CONFIG` | Path to the user configuration file |
| `CODEWALK_RUNS_DIR` | Where runs are stored |
| `CODEWALK_BACKEND` | Default backend |
| `CODEWALK_MODEL` | Model for every stage |
| `CODEWALK_BASE` | Default base branch |
| `CODEWALK_DEPTH` | Walkthrough depth |
| `CODEWALK_FORMAT` | Default output format |
| `CODEWALK_SERVER_HOST`, `CODEWALK_SERVER_PORT` | Server binding |
| `CODEWALK_OPEN_BROWSER` | Open a browser on `serve` |
| `CODEWALK_EDITOR`, `CODEWALK_GROUNDING`, `CODEWALK_GIT_HISTORY` | Toggle stages |
| `CODEWALK_MAX_STEPS` | Tool-use budget per stage |
| `NO_COLOR` | Disable terminal colour |

## Example: repository configuration

A repository can set sensible defaults for everyone working in it. This file is
safe to commit — it contains no credentials.

```toml
# .codewalk.toml
[defaults]
base = "develop"

[analysis]
# This repository vendors a large generated client; keep it out of diffs.
include_generated = false
```
