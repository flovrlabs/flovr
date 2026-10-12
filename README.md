# aia

aia runs workflows that are written as YAML files and kept in git. A workflow is a list of steps: ask a model for structured output, ask a decision model typed questions, send an HTTP request, or use a component that someone wrote once for all workflows. The same file holds the workflow's tests, which call the models for real.

One binary does everything:

| Command | What it does |
|---|---|
| `aia validate` | Checks a workflow without running it |
| `aia test` | Runs the tests in the workflow file |
| `aia run` | Runs the workflow once, for real |
| `aia serve` | Stays running, runs workflows when a request or a schedule starts them, and serves a page that shows them |

With `aia serve --edit` the page can also change the workflow files. Every change it makes is a small diff in the same YAML file.

## Try it

You need Go 1.26 and, for the examples, an [OpenRouter](https://openrouter.ai) key.

```bash
go build -o aia ./cmd/aia
```

Put the key in a file named `.env` in the folder you run aia from. The file is ignored by git.

```
OPENROUTER_API_KEY=...
```

Check an example, then run its tests. The tests call the models, which costs a little; they send none of the workflow's HTTP requests.

```bash
./aia validate examples/triage-ticket
```

```bash
./aia test examples/triage-ticket
```

Serve both examples and open http://127.0.0.1:8080. The two URLs are where `triage-ticket` would send its notifications; any address will do for looking around.

```bash
PAGER_URL=http://127.0.0.1:9/pager BILLING_URL=http://127.0.0.1:9/billing ./aia serve examples --edit
```

## A workflow

A workflow lives in a folder of its own, in a file named `workflow.yaml`. This is [examples/moderate-comment](examples/moderate-comment/workflow.yaml), without its comments and with one of its two tests:

```yaml
name: moderate-comment

trigger:
  webhook: /moderate
  input: schemas/comment.json

steps:
  - id: check
    decide:
      model: openrouter/cloudflare/clef
      input: ${trigger.body.text}
      questions:
        allowed:
          ask: Can this comment be published on a public product forum?
          yes_if: It is on topic or harmless, even if it is critical or rude in tone.
          no_if: It is spam, a threat, harassment, or gives away someone else's personal data.
        reason:
          ask: What is the main problem with the comment?
          choice:
            none: There is no problem with it.
            spam: Advertising, or links that have nothing to do with the discussion.
            abuse: Threats, harassment, or hateful language.
            private: Someone's personal data, such as a phone number or home address.

respond:
  body:
    allowed: ${check.allowed.value}
    reason: ${check.reason.value}

tests:
  - name: spam is refused
    fixture: fixtures/spam.json
    expect:
      response.body.allowed: false
      response.body.reason: spam
```

Paths in the file, such as `schemas/comment.json`, are relative to the workflow's folder.

### Trigger

| Trigger | The workflow starts when | Steps can read |
|---|---|---|
| `webhook: /path` | a POST request with a JSON body arrives at that path | `trigger.body` |
| `cron: "0 3 * * *"` | the schedule fires (five fields: minute, hour, day, month, weekday) | `trigger.time` |
| `manual: true` | you run it with `aia run` | what the fixture holds |

A webhook trigger can name a JSON Schema as `input`. A request whose body does not match it is answered with status 400 and a list of what is wrong, and nothing runs.

### Steps

Steps run in the order they are written. Each has an `id`, an optional `when`, and one kind of work.

| Kind | What it does | What later steps read from it |
|---|---|---|
| `ai` | Sends `input` to a model with the prompt in the file `prompt`, and holds the answer to the JSON Schema in the file `output` | the fields of the answer, such as `summarize.summary` |
| `decide` | Asks a decision model typed questions about `input`. It answers with probabilities and writes no text, which makes it fast and cheap | per question: `value`, and `probability` (yes/no), or `confidence` and `probabilities` (choice), plus `score` (score) |
| `http` | Sends a request: `method`, `url`, optional `headers` and `body` | `status` and `body` |
| `use` | Runs a component, giving it the values under `with` | what the component gives back |

A `decide` question is yes/no unless it has `choice` (a mapping of answer to meaning) or `score` (a list of levels from lowest to highest). `yes_if` and `no_if` tell a yes/no question what each answer means.

Models are named `provider/model`. The providers are `anthropic`, `openai` and `openrouter`, with keys in `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` and `OPENROUTER_API_KEY`. `OPENAI_BASE_URL` points `openai/...` models at another server that speaks the same API. Decision models come from `openrouter` only, through its Decisions API, which OpenRouter labels alpha.

### Expressions

`when` is a [CEL](https://cel.dev) expression that must be true for the step to run:

```yaml
when: classify.priority.value == "high" && classify.category.value in ["refund", "billing"]
```

Elsewhere, `${...}` inside a value is replaced by what the expression in it yields. A value that is one placeholder and nothing else keeps its type, so `${trigger.body}` passes the whole body on as an object. Write `$${` for a literal `${`.

An expression can read `trigger`, `env.NAME` for an environment variable, and every step before it by its id. A step that was skipped is `null`, so reading a field of it fails; test for it first:

```yaml
summary: '${summarize != null ? summarize.summary : ""}'
```

`trigger`, `env`, `response`, `input` and `output` cannot be used as step ids.

### Answering the caller

A workflow with `respond` is an endpoint: the request waits until the steps have run and is answered with `status` (200 if left out), `headers` and `body`, each of which can take values from the steps. If the workflow fails, the caller gets status 500 with the id of the run and nothing else; the reason is in the run's record.

Without `respond`, a webhook answers 202 at once and the workflow runs in the background. Add `?wait=true` to the request to get the finished run back instead.

### Tests

Each test runs the workflow with a fixture as the event that starts it, then compares. A fixture is a JSON file shaped like the event, for a webhook `{"body": {...}}`.

```yaml
tests:
  - name: urgent refund goes to billing, not on-call
    fixture: fixtures/urgent-refund.json
    expect:
      classify.category.value: refund
      classify.priority.value: high
      summarize: called
      alert_billing: called
      page_oncall: skipped
```

A key under `expect` is either a step's id, expecting it to be `called` or `skipped`, or an expression whose value is compared with what is written, as in `classify.priority.probabilities.high < 0.3: true`. A workflow with `respond` can expect things of its answer as `response.status` and `response.body`.

In a test, `ai` and `decide` steps call their models for real, because what the model answers is the thing being tested. `http` steps are not sent, and environment variables they would need do not have to be set.

## Components

A component is a step written once, in a file of its own, for any workflow to use. It lists the input it takes and does one kind of work. This is [notify](examples/triage-ticket/components/notify/component.yaml), which two steps of `triage-ticket` use:

```yaml
name: notify
description: Tells a team about an urgent ticket by sending it a summary.

input:
  url: string
  summary: string
  category: string

http:
  method: POST
  url: ${input.url}
  body:
    summary: ${input.summary}
    category: ${input.category}
```

A step uses it by the path of its folder, relative to the workflow file:

```yaml
- id: alert_billing
  when: classify.priority.value == "high"
  use: components/notify
  with:
    url: ${env.BILLING_URL}
    summary: ${summarize.summary}
    category: ${classify.category.value}
```

A component's expressions read `input.NAME` and `env.NAME` and nothing of the workflow that uses it, so it does the same wherever it is used. Input types are `string`, `number`, `boolean`, `object`, `list` and `any`.

### Components made of steps

Instead of `ai`, `decide` or `http`, a component can hold `steps`, written as in a workflow, and an `output` that says what it gives back. Its steps can use other components. The workflow that uses it sees only the output. [reply](examples/triage-ticket/components/reply/component.yaml) is one: it drafts a reply with a model, then hands the draft to a mail service.

```yaml
steps:
  - id: draft
    ai:
      model: openrouter/deepseek/deepseek-v4.1-flash
      prompt: prompt.md
      input: ${input.ticket}
      output: schema.json
  - id: send
    http:
      method: POST
      url: ${input.url}
      body:
        subject: ${draft.subject}
        text: ${draft.text}

output:
  subject: ${draft.subject}
```

### Testing a component

A component can carry tests of its own. Each gives it `input`, written out or as the path of a JSON file, and expects things of `output`. For a component made of steps, a test can also expect a step to be `called` or `skipped`.

```bash
./aia test examples/triage-ticket/components/reply
```

## What validate catches

`aia validate` reads the workflow, the components it uses and the files they name, and reports each problem with its line. Among them:

- a field that does not exist, a step without work, an id used twice
- a prompt, schema or fixture file that is missing, or a schema that is not valid
- an expression that does not parse, or that reads a step which has not run yet
- a field that a step does not put out, such as `classify.priorty` when the question is called `priority`, with the names that do exist
- input a component needs and is not given, is given and does not take, or is given with the wrong type
- a component that would end up using itself
- a test fixture that the trigger's `input` schema would refuse

`aia serve` checks the same things before it starts, and also that every environment variable the workflows read is set.

## The page

`aia serve` serves a page at its address. It shows each workflow as a drawing, with one shape per kind of step, and the recent runs. Selecting a run colours the drawing by what each step did; selecting a step shows what it put out, or why it failed.

With `--edit` the page becomes a place to work on the workflows:

- Select a step, the trigger or the response and change its values in the form. Saving writes only the lines of those values, so comments, blank lines and order in the file stay as they were.
- Add a step from one of the components in the workflow's folder, or remove a step.
- Open a component, which is drawn like a workflow with what it takes at the top and what it gives back at the bottom, and change its values the same way. The change is refused if it would break a workflow that uses the component.
- Run the tests of a workflow or of a component and see, for each, which expectations were met.
- Keep a run that went right as a test. Its request becomes the fixture, saved as a new file in the workflow's folder, and what the run did becomes what to expect: which steps ran, and what a model decided. Text a model wrote is left out unless you ask for it, since it reads differently each time. A test can also be written from nothing, or removed.
- See under **Changes** what git says has changed since the last commit.

Nothing is written unless the result is a workflow the server can go on serving; otherwise the page says what is wrong and the file stays as it was. The server also follows the files: what an editor or git changes is taken up without a restart, and a file that is no longer valid keeps being served as it was while the page says why.

`--edit` lets a page change files on the machine and start tests that cost model calls, so the server accepts it only on an address that this machine alone can reach.

## Serving for real

```bash
AIA_TOKEN=... ./aia serve workflows --addr 0.0.0.0:8080
```

- Without `AIA_TOKEN` the server listens only on this machine. With it, every webhook request must carry `Authorization: Bearer <token>`, and the browser asks for the token as a password.
- A webhook request must be sent with `Content-Type: application/json`. Its body can be up to 1 MB.
- A run that takes longer than five minutes is stopped.
- Finished runs are appended to `.aia/runs.jsonl`, readable by its owner only, and read back on start. They hold request bodies and step outputs, so keep the file out of git. `--history` puts it elsewhere.
- Every run records the git commit of the workflow files it ran.
- `/healthz` answers 200 while the server is up.

## In CI

[.github/workflows/ci.yml](.github/workflows/ci.yml) validates every example and, when the `OPENROUTER_API_KEY` secret is available, runs their tests and those of their components. The same two commands work for your own workflows:

```bash
aia validate path/to/workflow
```

```bash
aia test path/to/workflow
```

Both exit with a status other than 0 when something is wrong, and print what.

## What it does not do yet

- It does not retry a step that failed or queue runs; a run is one pass through the steps in one process.
- It has no steps for databases or message brokers. A workflow reaches other systems through `http`.
- It does not check signatures of webhooks from providers such as Stripe or GitHub.
- The page adds steps only from components, does not reorder steps, and does not add or remove the steps of a component.
- A webhook is always a POST to a fixed path.
