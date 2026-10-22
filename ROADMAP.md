# Roadmap

Flovr is in beta. Until 1.0 the format of the files, the commands and the page may still change from one release to the next, and a release that changes how a workflow file is written says so in its notes.

This file says what Flovr does now, what is being built, and what comes after. A ticked box is done; the rest is planned, in about the order it will be built. It changes with each release.

## Done

Workflows as files
- [x] A workflow is a YAML file in git: a trigger, steps, a response and tests
- [x] Steps that ask a model for structured output (`ai`), ask a decision model typed questions (`decide`), send a request (`http`), or use a component (`use`)
- [x] Expressions in CEL, checked before anything runs
- [x] `flovr validate`, `test`, `run` and `serve`; tests call the models for real
- [x] The answers of a round of tests recorded (`--record`) and replayed (`--replay`), so the examples, the page and CI can be tried without a key
- [x] A timeout for each step, and retries after the failures a step names: rate limits, outages, timeouts and answers that do not fit
- [x] The model's answer kept as it wrote it when it does not fit the schema, and shown with what was wrong; an empty answer is a failure

The server
- [x] Webhooks and schedules, with a token for webhooks and a key for the page
- [x] Runs recorded with the commit of the files they ran, and read back on start
- [x] Releases set up to build for macOS, Linux and Windows with GoReleaser
- [x] An installer for Linux and macOS (`curl | sh`) that checks what it downloads, a Homebrew cask and an image for Docker

The page
- [x] The lists of workflows and of services, where the page opens
- [x] A workflow drawn from left to right, coloured by the run on show
- [x] An editor of three columns for every node: what it can read, its settings, what it gave
- [x] Changes written into the file in place, keeping its comments and order, and only when the result is still valid
- [x] Steps of each kind added and removed; a new workflow; tests kept from runs, run and read
- [x] What starts a workflow switched between a request and a schedule; fields of the response added and taken out
- [x] A step tried on its own with the data of a past run
- [x] What git says has changed, under Changes
- [x] Light and dark

Services
- [x] Components, which the page calls services, written once and used by any workflow
- [x] A new service made from the page
- [x] Services taken from git, pinned to a commit in `flovr.lock` by `flovr get`, held to reading only their input and their own files
- [x] A market: a list of services in git, offered on the page and added as a step

## Now: 0.1, the first public release

- [ ] Move the repository to `flovrlabs/flovr`, open it and tag 0.1.0, which builds the binaries
- [ ] The Homebrew tap, `flovrlabs/homebrew-tap`, that the cask is published to
- [ ] Publish the market's list with the example services, pinned to the release
- [ ] The page of a service in the same editor as a workflow; its panel is still the old one
- [ ] Services made of steps, from the page

## Next: 0.2, tests to trust

A test that calls a model for real is one sample of what the model does. This release is about knowing what a test result means.

- [ ] Say on the page what is only saved, what is committed and what is live
- [ ] Run tests several times (`flovr test --repeat N`), with a pass rate for each test and each expectation
- [ ] Compare a round of tests with the last one at the commit, and show what got worse
- [ ] Keep a run that failed as a test, writing what should have happened

## Then: 0.3, run and debug from the page

- [ ] Run again with the same input, or from a step on, reusing what the steps before it gave
- [ ] A form to run a workflow now, built from its trigger's schema; the next times a schedule fires
- [ ] What changed, drawn on the steps of the workflow, with the history `git log` keeps
- [ ] Tokens, and an estimate of the cost, of each run and each round of tests
- [ ] Try a step with changes not saved yet
- [ ] A branch for "otherwise", and steps put in another order

## Later

- [ ] Runs queued so that a restart loses none
- [ ] Signatures of webhooks from Stripe, GitHub and others; methods other than POST
- [ ] Steps of your own code, run in a WebAssembly sandbox, so a custom step needs no trust in its author
- [ ] Steps for databases and message queues
- [ ] Workflow files that appear or go away taken up while the server runs
- [ ] Publishing a service to the market, and checks of what a listed one does
- [ ] A site for the docs, and a gallery of workflows to start from

## Not planned

Some things other tools do, Flovr does not mean to:

- An editor that writes the whole YAML file again. The page changes only the lines it must, so the file stays yours.
- A history of versions kept apart from git. `git log` is the history.
- Git, diffs or review only in a paid plan.
- Scores from 1 to 5 given by a model as what a test checks. A model may judge yes or no, one point at a time.

## Taking part

Bugs and ideas are welcome as issues. For a larger change, open an issue first, so we can agree on it before the work.
