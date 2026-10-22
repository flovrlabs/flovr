# Working on Flovr

This file is for anyone changing Flovr itself, people and coding agents alike. To write workflows with Flovr, read [skills/flovr/SKILL.md](skills/flovr/SKILL.md) instead.

## Checks

Run these before a commit; CI runs the same:

```bash
gofmt -l .
go vet ./...
go test ./...
```

`gofmt -l .` must print nothing. The tests call no model and need no key.

To try the command as built from the tree:

```bash
go build -o /tmp/flovr ./cmd/flovr
/tmp/flovr test examples/triage-ticket --replay
```

## Where things are

| Path | What it holds |
|---|---|
| `cmd/flovr` | The command: `validate`, `test`, `run`, `serve`, `get`, `schema`, `version` |
| `internal/workflow` | Reading and validating workflow and component files |
| `internal/expr` | CEL expressions and `${...}` placeholders |
| `internal/engine` | Running the steps of a workflow, with timeouts, retries and tokens |
| `internal/ai` | The model providers, and the recording and replay of answers |
| `internal/server` | Webhooks, schedules, runs, and the API behind the page |
| `internal/server/ui` | The page: plain JavaScript and CSS, embedded in the binary |
| `internal/yamledit` | Changing single values in a YAML file without writing the rest again |
| `internal/fetch` | Taking components from git and pinning them in `flovr.lock` |
| `schema` | JSON Schemas of workflow and component files |
| `examples` | Workflows with tests and recorded answers, run in CI |
| `market/index.json` | The list of services the page offers |
| `docs/media` | The pictures and recordings in the README, and the scripts that make them |
| `skills/flovr` | The skill that teaches a coding agent to write workflows |

## Rules of the code

- Write messages, comments and docs in plain English a newcomer can read: say what happened and what to do, not the name of an internal type. A message names the file and line when it is about a file.
- Name a test after the behaviour it checks, as `TestAnAnswerAStepCouldNotUseIsKept`.
- The page has no build step and no dependencies. Its Content-Security-Policy allows scripts and styles from the server only, so no inline scripts, inline styles or outside fonts.
- The page changes a workflow through `internal/yamledit`, which changes only the lines it must and keeps comments and order. Never write the whole file again.
- A change to the fields of a workflow or component file needs the same change in `schema/workflow.json` or `schema/component.json`. A test compares the schemas with the Go types and checks every valid file in the repository against them.
- A change to the file format that an older flovr cannot read raises `workflow.Format`.
- Keep `README.md` and `ROADMAP.md` true: a feature that is done is ticked and described; a limit that is gone is taken out of "What it does not do yet".

## Things that cost money or leak

- `flovr test` without `--replay`, `flovr test --record` and `flovr run` call models and cost money. The checks above do not.
- After a change to the prompt, input or schema of an example, record its answers again with `flovr test --record examples/<name>`, which needs `OPENROUTER_API_KEY`. Never edit `answers.jsonl` by hand.
- Never commit `.env` or `.flovr/`: the runs kept there hold the bodies of requests, and those may hold secrets.
- After a change to the page, make the README's pictures again with `sh docs/media/record/record.sh`. It replays answers and calls no model.
