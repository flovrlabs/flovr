---
name: flovr
description: Write, change and test Flovr AI workflows, the workflow.yaml and component.yaml files that the flovr command validates, tests and serves. Use when a repository has such files, or when asked to build an automation that calls a model, decides, sends HTTP requests or reuses a component.
---

# Writing Flovr workflows

A Flovr workflow is a `workflow.yaml` file in git: what starts it, steps that run in order, an optional answer to the caller, and tests in the same file. The `flovr` command checks, tests, runs and serves it. Every change you make is a diff a person reviews, so keep changes small and the file readable.

## The loop

1. Edit the file.
2. Run `flovr validate --json <folder>`. Fix every problem at the line it gives, then run it again until `"ok": true`. Validation checks expressions, the files a step names and the components it uses, more than the JSON Schema can.
3. Run the tests:
   - `flovr test --replay <folder>` answers from `answers.jsonl` and calls no model. It is free, needs no key, and checks the wiring: expressions, which steps run, what the workflow answers. A request that was not recorded, because a prompt, an input or a schema changed, cannot be replayed.
   - `flovr test <folder>` calls the models for real and costs money. Ask before running it more than once, and use `--max-tokens` to cap a round.
   - `flovr test --repeat 5 <folder>` runs each test five times, since one passing run of a model proves little. `--json` gives the outcome to read.
4. After changing a prompt, an input or a schema, `flovr test --record <folder>` keeps the new answers in `answers.jsonl` if every test passes. It calls the models. Never write `answers.jsonl` by hand.

`flovr schema` prints the JSON Schema of workflow files and `flovr schema component` that of components.

## A workflow file

```yaml
# yaml-language-server: $schema=https://raw.githubusercontent.com/flovrlabs/flovr/main/schema/workflow.json
flovr: 1
name: triage-ticket

trigger:
  webhook: /tickets            # or cron: "0 9 * * 1", or manual: true
  input: schemas/ticket.json   # optional: a body that does not match is refused

steps:
  - id: classify
    decide:
      model: openrouter/cloudflare/clef
      input: ${trigger.body}
      questions:
        category:
          ask: What does the customer need?
          choice:
            refund: They want money back for a charge.
            other: Anything else.
        urgent:
          ask: Can the customer not use the product right now?

  - id: summarize
    when: classify.urgent.value
    timeout: 30s
    retry: 2
    ai:
      model: openrouter/deepseek/deepseek-v4.1-flash
      prompt: prompts/summarize.md
      input: ${trigger.body}
      output: schemas/summary.json

  - id: page_oncall
    when: classify.urgent.value && classify.category.value != "refund"
    use: components/notify
    with:
      url: ${env.PAGER_URL}
      summary: ${summarize.summary}

tests:
  - name: an outage pages on-call
    fixture: fixtures/outage.json
    expect:
      classify.urgent.value: true
      page_oncall: called
```

## Steps

Each step has an `id` (letters, digits, underscores; not `trigger`, `env`, `response`, `input` or `output`), an optional `when`, and one of:

| Kind | Fields | Later steps read |
|---|---|---|
| `ai` | `model`, `prompt` (a file), `input`, `output` (a JSON Schema file) | the fields of the answer, as `summarize.summary` |
| `decide` | `model` (an openrouter decision model), `input`, `questions` | per question `value`, and `probability` (yes/no) or `confidence` and `probabilities` (choice, score), plus `score` for a score |
| `http` | `method`, `url`, optional `headers` and `body` | `status` and `body` |
| `use` | the folder of a component, and `with` for its input | what the component gives back |

- A `decide` question is yes/no unless it has `choice` (answer: meaning) or `score` (a list of levels from lowest to highest). `yes_if` and `no_if`, given together, say what each answer of a yes/no question means.
- Models are `provider/model`, with providers `anthropic`, `openai` and `openrouter`. Decision models come from `openrouter` only.
- Put each prompt in a file and each output schema in a file next to the workflow. A schema with `"additionalProperties": false` and `required` keeps answers tight.
- `timeout` limits each try. `retry: 2`, or `retry: {times: 2, wait: 2s, on: [rate-limit, unavailable, timeout, invalid-answer]}`, tries again after those failures only. Do not add `retry` to an `http` step that must not run twice, such as a payment.

## Expressions

- `when` is a [CEL](https://cel.dev) expression. Everywhere else, `${...}` inside a value is replaced by its expression; a value that is one placeholder keeps its type, so `${trigger.body}` passes an object on.
- Expressions read `trigger` (`trigger.body` for a webhook), `env.NAME`, and every earlier step by its id. A skipped step is `null`: write `${summarize != null ? summarize.summary : ""}` when it may have been skipped.
- Secrets come from `${env.NAME}`. Never write a key, a token or a password into a file.

## Answering the caller

`respond: {status: 200, headers: {...}, body: {...}}` makes the workflow an endpoint: the request waits for the steps and gets this answer. Values in it can read every step.

## Tests

A test runs the workflow with a fixture, a JSON file shaped like the event: `{"body": {...}}` for a webhook. `expect` holds:

- a step's id with `called` or `skipped`;
- an expression with the value it should give, as `classify.category.value: refund` or `classify.urgent.probability > 0.8: true`;
- for a workflow with `respond`, `response.status` and `response.body...`.

Expect what a model decides or which steps run, not the words a model writes, which change from run to run. `http` steps are not sent in tests.

## Components

A component, `component.yaml` in a folder of its own, is a step written once for any workflow to use:

```yaml
flovr: 1
name: notify
description: Tells a team about an urgent ticket.
input:
  url: string
  summary: string
http:
  method: POST
  url: ${input.url}
  body:
    summary: ${input.summary}
tests:
  - name: it posts the summary
    input: {url: "http://x", summary: down}
    expect:
      output.status: 200
```

- Input types are `string`, `number`, `boolean`, `object`, `list` and `any`. Inside, expressions read `input.NAME` and `env.NAME` and nothing of the workflow using it.
- Instead of one `ai`, `decide` or `http`, a component can hold `steps` and an `output` that says what it gives back.
- A component in git is used as `use: github.com/owner/repo/dir@v1.2`, and `flovr get` pins it in `flovr.lock`.
- Test one with `flovr test path/to/component.yaml`.
