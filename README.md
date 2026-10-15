<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/brand/logo-dark.png">
    <img src="docs/brand/logo-light.png" alt="Flovr" width="220">
  </picture>
</h1>

Flovr (say *flow-ver*: flows, versioned) runs workflows that are written as YAML files and kept in git. A workflow is a list of steps: ask a model for structured output, ask a decision model typed questions, send an HTTP request, or use a component that someone wrote once for all workflows. The same file holds the workflow's tests, which call the models for real.

Flovr is in beta: the format of the files may still change before 1.0. [ROADMAP.md](ROADMAP.md) says what is done, what is being built and what comes next.

One binary does everything:

| Command | What it does |
|---|---|
| `flovr validate` | Checks a workflow without running it |
| `flovr test` | Runs the tests in the workflow file |
| `flovr run` | Runs the workflow once, for real |
| `flovr serve` | Stays running, runs workflows when a request or a schedule starts them, and serves a page that shows them |

With `flovr serve --edit` the page can also change the workflow files. Every change it makes is a small diff in the same YAML file.

Flovr is new. This is its first release, and the file format may still change before 1.0.

## Install

On Linux or macOS:

```bash
curl -fsSL https://raw.githubusercontent.com/flovrlabs/flovr/main/install.sh | sh
```

[install.sh](install.sh) downloads the archive of the latest release for your system, checks it against the release's checksums, and puts `flovr` in `~/.local/bin`. `FLOVR_VERSION=v0.1.0` installs another release, and `FLOVR_INSTALL_DIR` puts it in another folder.

With Homebrew:

```bash
brew install flovrlabs/tap/flovr
```

With Docker, where the examples come with the image and run without a key:

```bash
docker run --rm ghcr.io/flovrlabs/flovr test /examples/triage-ticket --replay
```

Or download the archive for your system from [Releases](https://github.com/flovrlabs/flovr/releases), the zip on Windows, and put `flovr` somewhere on your PATH. Or build it with Go 1.26 or later:

```bash
go install github.com/flovrlabs/flovr/cmd/flovr@latest
```

## Try it

The examples are in this repository.

```bash
git clone https://github.com/flovrlabs/flovr && cd flovr
```

### Without a key

Each example keeps, in `answers.jsonl` next to its workflow, what the models answered when its tests were last recorded. With `--replay`, flovr gives the steps those answers and calls no model, so nothing is needed but the binary, and nothing is paid.

```bash
flovr test examples/triage-ticket --replay
```

Serve both examples and open the page address the server prints, which ends with `?key=`. Run their tests from the page, or open a step to see what it read and gave. The two URLs are where `triage-ticket` would send its notifications; any address will do for looking around.

```bash
PAGER_URL=http://127.0.0.1:9/pager BILLING_URL=http://127.0.0.1:9/billing flovr serve examples --edit --replay
```

An answer is replayed only for exactly the request it was given for. Change a prompt, an input or a fixture, and that step has no answer until the tests are recorded again, which takes a key.

### With a key

The models are called for real with an [OpenRouter](https://openrouter.ai) key. Put the key in a file named `.env` in the folder you run flovr from. The file is ignored by git. A `.env` file cannot say where keys are sent: it sets `OPENAI_BASE_URL` or `ANTHROPIC_BASE_URL` only if it gives that provider's key too, and never a proxy.

```
OPENROUTER_API_KEY=...
```

Check an example, then run its tests. The tests call the models, which costs a little; they send none of the workflow's HTTP requests.

```bash
flovr validate examples/triage-ticket
```

```bash
flovr test examples/triage-ticket
```

Serve both examples as above, without `--replay`, and the page's tests call the models too.

```bash
PAGER_URL=http://127.0.0.1:9/pager BILLING_URL=http://127.0.0.1:9/billing flovr serve examples --edit
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
| `manual: true` | you run it with `flovr run` | what the fixture holds |

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

An expression can read `trigger`, `env.NAME` for an environment variable, and every step before it by its id. A variable is read only by its name, as `env.NAME`: `env` as a whole, or `env[...]`, is refused, so nothing in a request can choose which variable a workflow reads. A step that was skipped is `null`, so reading a field of it fails; test for it first:

```yaml
summary: '${summarize != null ? summarize.summary : ""}'
```

`trigger`, `env`, `response`, `input` and `output` cannot be used as step ids.

### Answering the caller

A workflow with `respond` is an endpoint: the request waits until the steps have run and is answered with `status` (200 if left out), `headers` and `body`, each of which can take values from the steps. If the workflow fails, the caller gets status 500 with the id of the run and nothing else; the reason is in the run's record.

Without `respond`, a webhook answers 202 at once and the workflow runs in the background. Add `?wait=true` to the request to wait until it has finished: a server without a token then answers with the whole run, and one with a token with the run's id and whether it succeeded.

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

`flovr test --record` keeps what the models answered in `answers.jsonl` next to the workflow, if every test passed. `flovr test --replay` then runs the tests with those answers instead of calling the models. A replayed test checks the workflow, its expressions, which steps run and what it answers, but not the models; it needs no key and costs nothing. Commit the file with the workflow, so that others and CI can run the tests without a key. Answers are recorded only from tests, never from the runs of a server.

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
flovr test examples/triage-ticket/components/reply
```

### Components from git

A step can use a component kept in a git repository, named by host, owner and repository, then the component's folder in it, then `@` and a tag, branch or commit:

```yaml
- id: tell
  use: github.com/acme/flovr-services/slack-notify@v1.2
  with:
    webhook: ${env.SLACK_WEBHOOK}
    text: ${summarize.text}
```

`flovr get` fetches what the workflows in a folder take from git, and writes the commit each tag or branch is at in `flovr.lock` in that folder:

```bash
flovr get workflows
```

Keep `flovr.lock` in git with the workflows. Running, testing and serving never go to the network: they take each component at the commit the lock file pins, from a cache on the machine (`FLOVR_CACHE` sets where), and say to run `flovr get` when it is not there. A tag that moves on is not followed until `flovr get -u`. A deploy runs `flovr get` before it serves.

What comes from git is held to more than what you write yourself:

- It reads only its input, never `env`, which holds the server's secrets. The workflow gives it what it needs, as `with` does above.
- The files it names stay inside its repository, and a link in the repository is taken as a plain file.
- It does not use another component from git.
- The page shows where it comes from and does not change it: it is changed at its source.

It can still send what it is given to the URLs it names, and the models it asks are paid for with your keys, so read a component before you use it, as you would any code.

### The market

The market is a list of components in git that anyone may use. The page lists them beside your own, and adding a step from one takes it from git and pins it in `flovr.lock`, as `flovr get` does. The list is one JSON file, kept anywhere it can be read over https, or a file on the machine; `flovr serve --edit --market <address>` says where, and [market/index.json](market/index.json) in this repository is the one read unless told otherwise:

```json
{"services": [{
  "name": "notify",
  "description": "Tells a team about an urgent ticket by sending it a summary.",
  "use": "github.com/flovrlabs/flovr/examples/triage-ticket/components/notify@main",
  "kind": "http",
  "inputs": [{"name": "url", "type": "string"}, {"name": "summary", "type": "string"}, {"name": "category", "type": "string"}],
  "tags": ["alert", "webhook"]
}]}
```

What the list says is only what it offers: a component is what its repository holds at the commit taken, and is held to the rules above. To list one, add it to the file in a pull request.

## What validate catches

`flovr validate` reads the workflow, the components it uses and the files they name, and reports each problem with its line. Among them:

- a field that does not exist, a step without work, an id used twice
- a prompt, schema or fixture file that is missing, or a schema that is not valid
- an expression that does not parse, or that reads a step which has not run yet
- a field that a step does not put out, such as `classify.priorty` when the question is called `priority`, with the names that do exist
- input a component needs and is not given, is given and does not take, or is given with the wrong type
- a component that would end up using itself
- a test fixture that the trigger's `input` schema would refuse

`flovr serve` checks the same things before it starts, and also that every environment variable the workflows read is set.

## The page

`flovr serve` serves a page and prints its address, which carries the page's key as `?key=...`. The browser keeps the key once the page has been opened with it, and nothing reaches the page or its API without it, not even another program on the same machine. The key is kept for you outside the project, so it stays the same across restarts; `FLOVR_PAGE_KEY` sets it instead.

The page opens on a list of the workflows and one of the services, the components that workflows use. A workflow is drawn from left to right, with one shape and colour per kind of step, and has its runs and its tests. Picking a run colours the drawing by what each step did; a step opens to show what it read and put out, or why it failed.

With `--edit` the page becomes a place to work on the workflows:

- Open a step, the trigger or the response and change its values in the editor, with what it can read on one side and what it gave on the other. Saving writes only the lines of those values, so comments, blank lines and order in the file stay as they were.
- Switch what starts a workflow between a request and a schedule, and add a field to the response's body or take one out. These are written at once, as adding a step is.
- Make a new workflow: give it a name, say what starts it, and choose a component for its first step. It is written as a folder of that name holding a `workflow.yaml`, in the folder the server was started with, and is served at once.
- Add a step that asks a model to write or to decide, sends an HTTP request, or uses a component; or remove a step. The components on offer are those in the folders the server was started with, so a workflow can use a component that lives in another workflow's folder.
- Open a component, which is drawn like a workflow with what it takes at the top and what it gives back at the bottom, and change its values the same way. The change is refused if it would break a workflow that uses the component.
- Make a new service: a name, what it takes, and one piece of work. It is written as `components/<name>/component.yaml` in the folder the server was started with, and every workflow can use it at once.
- Browse the market and add a step from one of its services: it is taken from git and pinned in `flovr.lock`.
- Run the tests of a workflow or of a component and see, for each, which expectations were met.
- Keep a run that went right as a test. Its request becomes the fixture, saved as a new file in the workflow's folder, and what the run did becomes what to expect: which steps ran, and what a model decided. Text a model wrote is left out unless you ask for it, since it reads differently each time. A test can also be written from nothing, or removed.
- See under **Changes** what git says has changed since the last commit.

Nothing is written unless the result is a workflow the server can go on serving; otherwise the page says what is wrong and the file stays as it was. The server also follows the files: what an editor or git changes is taken up without a restart, and a file that is no longer valid keeps being served as it was while the page says why.

`--edit` lets a page change files on the machine and start tests that cost model calls, so the server accepts it only on an address that this machine alone can reach.

## Serving for real

```bash
FLOVR_TOKEN=... flovr serve workflows --addr 0.0.0.0:8080
```

- Without `FLOVR_TOKEN` the server listens only on this machine. With it, every webhook request must carry `Authorization: Bearer <token>`. The token must be at least 32 characters; `openssl rand -hex 32` makes one.
- The token is for whatever sends the webhooks, so it starts workflows and does nothing else. The page needs its own key, as above; set `FLOVR_PAGE_KEY` on a server so you know it.
- Serve over TLS with `--tls-cert` and `--tls-key`, or behind a proxy that adds it, so that the token and the key do not travel in the clear.
- A webhook request must be sent with `Content-Type: application/json`. Its body can be up to 1 MB.
- At most 16 runs started by webhooks go at once, and a request beyond that is answered 429; `--max-runs` changes the number. A run that takes longer than five minutes is stopped.
- An `http` step follows a redirect only to the same host, and never from https to http, so a key in a header does not reach another server. It can reach any address the server can, so do not build its URL from what a request or a model says.
- Finished runs are appended to `.flovr/runs.jsonl`, readable by its owner only, and read back on start. They hold request bodies and step outputs; the folder gets a `.gitignore` that keeps them out of git. `--history` puts the file elsewhere.
- Every run records the git commit of the workflow files it ran.
- `/healthz` answers 200 while the server is up.

The image serves the folder mounted at `/work` on port 8080:

```bash
docker run -p 8080:8080 -v "$PWD/workflows:/work" -e FLOVR_TOKEN -e FLOVR_PAGE_KEY -e OPENROUTER_API_KEY ghcr.io/flovrlabs/flovr
```

It runs as a user of its own, uid 10001, and writes its runs to `/work/.flovr`, so that user must be able to write there; `--user "$(id -u):$(id -g)"` runs it as you instead. Give `FLOVR_PAGE_KEY`, since a key the server made itself is lost with the container.

## In CI

[.github/workflows/ci.yml](.github/workflows/ci.yml) validates every example and, when the `OPENROUTER_API_KEY` secret is available, runs their tests and those of their components. Without the secret, as in pull requests from forks, it runs them with `--replay`. The same commands work for your own workflows:

```bash
flovr validate path/to/workflow
```

```bash
flovr test path/to/workflow
```

Both exit with a status other than 0 when something is wrong, and print what.

## What it does not do yet

- It does not retry a step that failed or queue runs; a run is one pass through the steps in one process.
- It has no steps for databases or message brokers. A workflow reaches other systems through `http`.
- It does not check signatures of webhooks from providers such as Stripe or GitHub.
- The page does not reorder steps, does not add or remove the steps of a component, and makes only services that do one piece of work. A workflow it makes starts with one step and has no `respond`.
- A workflow file that appears or goes away while the server runs is not noticed until the server is restarted, unless the page made it.
- A webhook is always a POST to a fixed path.

## License

Apache License 2.0. See [LICENSE](LICENSE).
