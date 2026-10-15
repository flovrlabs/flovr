# Recordings and pictures

The README shows these:

| File | What it shows |
|---|---|
| `demo.gif` | The page: a workflow opened, a step looked into, a field added to the response and seen as a one-line git diff, the tests run |
| `terminal.gif` | `flovr validate` and `flovr test --replay` with no model key |
| `canvas.png` | triage-ticket drawn and coloured by its last run, dark |
| `editor.png` | The editor of a step, light |

They are made, not drawn: after a change to the page, make them again with

```bash
sh docs/media/record/record.sh
```

It serves a copy of the examples with `--replay`, so no model is called, and drives headless Chrome through the steps in [record/record.mjs](record/record.mjs); [record/terminal.tape](record/terminal.tape) is the terminal's, for [vhs](https://github.com/charmbracelet/vhs). It needs Go, Node 22 or later, Google Chrome, ffmpeg, vhs and git, and takes about a minute.
