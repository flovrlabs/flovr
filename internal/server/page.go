package server

import (
	"crypto/subtle"
	"embed"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"aia/internal/ai"
	"aia/internal/workflow"
)

//go:embed ui
var ui embed.FS

// page serves the web page and the JSON it shows and, if the server allows
// editing, takes the changes the page makes.
func (s *Server) page(rw http.ResponseWriter, r *http.Request) {
	if s.opts.Edit && r.Method == http.MethodPost && slices.Contains([]string{"/api/edit", "/api/steps", "/api/prompt"}, r.URL.Path) {
		s.change(rw, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		reply(rw, http.StatusNotFound, map[string]string{"error": "no workflow has this webhook path"})
		return
	}
	if !s.mayView(r) {
		rw.Header().Set("WWW-Authenticate", `Basic realm="aia", charset="UTF-8"`)
		reply(rw, http.StatusUnauthorized, map[string]string{"error": "sign in with any user name and the server's token as the password"})
		return
	}
	h := rw.Header()
	// Run records show text from outside: tickets, model answers. The page
	// loads nothing but its own files, so none of that text can run as code.
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")

	switch path := r.URL.Path; {
	case path == "/":
		s.file(rw, "ui/index.html", "text/html; charset=utf-8")
	case path == "/ui/app.js":
		s.file(rw, "ui/app.js", "text/javascript; charset=utf-8")
	case path == "/ui/app.css":
		s.file(rw, "ui/app.css", "text/css; charset=utf-8")
	case path == "/api/state":
		reply(rw, http.StatusOK, s.state())
	case strings.HasPrefix(path, "/api/runs/"):
		run, ok := s.history.get(strings.TrimPrefix(path, "/api/runs/"))
		if !ok {
			reply(rw, http.StatusNotFound, map[string]string{"error": "no such run"})
			return
		}
		reply(rw, http.StatusOK, run)
	case path == "/api/prompt" && s.opts.Edit:
		s.prompt(rw, r)
	case path == "/api/changes" && s.opts.Edit:
		s.changes(rw, r)
	default:
		reply(rw, http.StatusNotFound, map[string]string{"error": "nothing is served at this path"})
	}
}

// mayView reports whether the request may see the page. A browser cannot be
// told to send a bearer token, so the token is also accepted as the password
// of HTTP basic authentication.
func (s *Server) mayView(r *http.Request) bool {
	if s.opts.Token == "" {
		return true
	}
	given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if _, password, ok := r.BasicAuth(); ok {
		given = password
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(s.opts.Token)) == 1
}

func (s *Server) file(rw http.ResponseWriter, name, contentType string) {
	data, err := ui.ReadFile(name)
	if err != nil {
		reply(rw, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	rw.Header().Set("Content-Type", contentType)
	rw.Write(data)
}

// stateView is everything the page shows except the detail of one run.
type stateView struct {
	Commit    string         `json:"commit"`
	Protected bool           `json:"protected"` // whether requests need the token
	Editable  bool           `json:"editable"`  // whether the page may change the files
	Workflows []workflowView `json:"workflows"`
	Runs      []Run          `json:"runs"` // newest first, without triggers and outputs
}

type workflowView struct {
	Name    string     `json:"name"`
	Path    string     `json:"path"`
	Trigger string     `json:"trigger"` // such as "POST /tickets"; empty if it is not served
	Tests   int        `json:"tests"`
	Steps   []stepView `json:"steps"`

	// For a workflow that is an endpoint: the schema a request body must
	// match, as the trigger names it, and what the caller gets back.
	Input   string       `json:"input,omitempty"`
	Respond *respondView `json:"respond,omitempty"`

	// Sample is a request body the workflow can be tried with: that of its
	// first test.
	Sample any `json:"sample,omitempty"`

	// Only when the page may change the files.
	Rev     string      `json:"rev,omitempty"`     // identifies the file's content; sent back with a change
	Problem string      `json:"problem,omitempty"` // why what the file holds now is not being served
	Edit    []fieldView `json:"edit,omitempty"`    // the values of the trigger

	// Components are those a step can be added from: the ones in the
	// workflow's folder.
	Components []componentView `json:"components,omitempty"`
}

type stepView struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Subtitle  string         `json:"subtitle"` // a few words for the step's box
	When      string         `json:"when,omitempty"`
	Reads     []string       `json:"reads"`  // the trigger and earlier steps it uses
	Fields    [][2]string    `json:"fields"` // label and value pairs describing it
	Questions []questionView `json:"questions,omitempty"`
	Edit      []fieldView    `json:"edit,omitempty"` // only when the page may change the files

	// For a step that uses a component: the component's name and its path
	// as the step gives it.
	Component string `json:"component,omitempty"`
	Uses      string `json:"uses,omitempty"`
}

// respondView is the answer a workflow gives its caller, as written.
type respondView struct {
	Status int         `json:"status"`
	Body   any         `json:"body"`           // with its ${...} unresolved
	Reads  []string    `json:"reads"`          // the trigger and steps it uses
	Edit   []fieldView `json:"edit,omitempty"` // only when the page may change the files
}

// What a field holds, which decides how the page lets it be changed.
const (
	kindText       = "text"
	kindExpression = "expression"
	kindNumber     = "number" // a whole number
	kindChoice     = "choice" // one of Options
	kindPrompt     = "prompt" // Value names a file; its text is at /api/prompt
)

// fieldView is one value of a workflow that the page may change.
type fieldView struct {
	Label    string   `json:"label"`
	Group    string   `json:"group,omitempty"` // what it belongs to, such as a question
	Path     []any    `json:"path"`            // where the value is in the workflow file
	Value    string   `json:"value"`
	Kind     string   `json:"kind"`
	Options  []string `json:"options,omitempty"`
	Optional bool     `json:"optional,omitempty"` // saving it empty removes it
	Hint     string   `json:"hint,omitempty"`     // a line of help, such as what leaving it empty means
}

type questionView struct {
	Name    string      `json:"name"`
	Ask     string      `json:"ask"`
	Answers string      `json:"answers"` // such as "one of refund, billing"
	Options []ai.Option `json:"options,omitempty"`
	YesIf   string      `json:"yesIf,omitempty"`
	NoIf    string      `json:"noIf,omitempty"`
}

func (s *Server) state() stateView {
	if s.opts.Edit {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.refresh()
	}
	view := stateView{
		Commit: s.commit(), Protected: s.opts.Token != "", Editable: s.opts.Edit,
		Workflows: []workflowView{}, Runs: s.history.recent(),
	}
	for i := range view.Runs {
		r := &view.Runs[i]
		r.Trigger, r.Response = nil, nil
		r.Steps = slices.Clone(r.Steps)
		for j := range r.Steps {
			r.Steps[j].Output = nil
		}
	}
	for _, w := range s.live.Load().workflows {
		view.Workflows = append(view.Workflows, s.view(w))
	}
	return view
}

// view describes w for the page. If the server allows editing, the caller
// holds s.mu.
func (s *Server) view(w *workflow.Workflow) workflowView {
	view := describe(w)
	src := s.sources[w.Path]
	if src == nil {
		// Nothing can be changed, so the page is not told what could be.
		view.Edit = nil
		for i := range view.Steps {
			view.Steps[i].Edit = nil
		}
		if view.Respond != nil {
			view.Respond.Edit = nil
		}
		return view
	}
	view.Rev, view.Problem = src.rev, src.problem
	view.Components = catalogue(w)
	return view
}

func describe(w *workflow.Workflow) workflowView {
	view := workflowView{Name: w.Name, Path: w.Path, Tests: len(w.Tests), Steps: []stepView{}}
	// offer lists a value as one the page may change, unless the way it is
	// written in the file rules that out.
	offer := func(to *[]fieldView, f fieldView) {
		if !w.Writable(f.Path...) {
			return
		}
		if f.Kind == "" {
			f.Kind = kindText
		}
		*to = append(*to, f)
	}

	if len(w.Tests) > 0 {
		if event, err := w.Fixture(w.Tests[0].Fixture); err == nil {
			if fields, ok := event.(map[string]any); ok {
				view.Sample = fields["body"]
			}
		}
	}
	// texts offers every piece of text in v, however deep it is.
	var texts func(to *[]fieldView, group, label string, path []any, v any)
	texts = func(to *[]fieldView, group, label string, path []any, v any) {
		switch v := v.(type) {
		case string:
			if label == "" {
				label = group
			}
			offer(to, fieldView{Label: label, Group: group, Path: path, Value: v})
		case map[string]any:
			for _, key := range slices.Sorted(maps.Keys(v)) {
				name := key
				if label != "" {
					name = label + "." + key
				}
				texts(to, group, name, append(slices.Clone(path), key), v[key])
			}
		}
	}

	if r := w.Respond; r != nil {
		rv := &respondView{Status: r.Status, Body: r.Body, Reads: w.RespondReads()}
		status := ""
		if r.Status != 0 {
			status = strconv.Itoa(r.Status)
		} else {
			rv.Status = http.StatusOK
		}
		offer(&rv.Edit, fieldView{
			Label: "Status", Path: []any{"respond", "status"}, Value: status,
			Kind: kindNumber, Optional: true, Hint: "Empty means 200.",
		})
		for _, name := range slices.Sorted(maps.Keys(r.Headers)) {
			offer(&rv.Edit, fieldView{Label: name, Group: "Headers", Path: []any{"respond", "headers", name}, Value: r.Headers[name]})
		}
		texts(&rv.Edit, "Body", "", []any{"respond", "body"}, r.Body)
		view.Respond = rv
	}
	switch t := w.Trigger; {
	case t != nil && t.Webhook != "":
		view.Trigger, view.Input = "POST "+t.Webhook, t.Input
		offer(&view.Edit, fieldView{Label: "Webhook path", Path: []any{"trigger", "webhook"}, Value: t.Webhook})
		offer(&view.Edit, fieldView{
			Label: "Body schema", Path: []any{"trigger", "input"}, Value: t.Input, Optional: true,
			Hint: "A JSON Schema file. A request whose body does not match it is refused. Empty takes any body.",
		})
	case t != nil && t.Cron != "":
		view.Trigger = "cron " + t.Cron
		offer(&view.Edit, fieldView{Label: "Schedule", Path: []any{"trigger", "cron"}, Value: t.Cron})
	}

	for i, s := range w.Steps {
		step := stepView{ID: s.ID, Kind: s.Kind(), When: s.When, Reads: w.Reads(i), Fields: [][2]string{}}
		if step.Reads == nil {
			step.Reads = []string{}
		}
		add := func(label, value string) {
			if value != "" {
				step.Fields = append(step.Fields, [2]string{label, value})
			}
		}
		at := func(path ...any) []any { return append([]any{"steps", i}, path...) }
		edit := func(f fieldView) { offer(&step.Edit, f) }
		texts := func(group, label string, path []any, v any) { texts(&step.Edit, group, label, path, v) }

		edit(fieldView{
			Label: "Run only when", Path: at("when"), Value: s.When, Kind: kindExpression, Optional: true,
			Hint: "Empty means the step always runs.",
		})
		if c := s.Component(); c != nil {
			// What the component does is shown but is not the workflow's to
			// change; the input the step gives it is.
			step.Component, step.Uses, step.Subtitle = c.Name, s.Use, c.Name
			add("component", s.Use)
			switch {
			case c.AI != nil:
				add("model", c.AI.Model)
			case c.Decide != nil:
				add("model", c.Decide.Model)
				step.Questions = questions(c.Decide)
			case c.HTTP != nil:
				add("request", strings.ToUpper(c.HTTP.Method)+" "+c.HTTP.URL)
			}
			for _, p := range c.Input {
				texts("Input", p.Name, at("with", p.Name), s.With[p.Name])
			}
		}
		switch {
		case s.AI != nil:
			step.Subtitle = shortModel(s.AI.Model)
			add("model", s.AI.Model)
			add("prompt", s.AI.Prompt)
			add("output", s.AI.Output)
			edit(fieldView{Label: "Model", Path: at("ai", "model"), Value: s.AI.Model})
			edit(fieldView{Label: "Prompt", Path: at("ai", "prompt"), Value: s.AI.Prompt, Kind: kindPrompt})
			texts("", "Input", at("ai", "input"), s.AI.Input)
			edit(fieldView{Label: "Output schema", Path: at("ai", "output"), Value: s.AI.Output})
		case s.Decide != nil:
			step.Subtitle = shortModel(s.Decide.Model)
			add("model", s.Decide.Model)
			edit(fieldView{Label: "Model", Path: at("decide", "model"), Value: s.Decide.Model})
			texts("", "Input", at("decide", "input"), s.Decide.Input)
			step.Questions = questions(s.Decide)
			for _, name := range slices.Sorted(maps.Keys(s.Decide.Questions)) {
				q := s.Decide.Questions[name]
				spec := q.Spec()

				in := func(path ...any) []any { return append(at("decide", "questions", name), path...) }
				edit(fieldView{Label: "Question", Group: name, Path: in("ask"), Value: q.Ask})
				list := "choice"
				if q.Score != nil {
					list = "score"
				}
				for j, o := range spec.Options {
					// An answer's meaning is the value of its name, in a
					// mapping of answers or in an item of a list of them.
					edit(fieldView{Label: o.Name, Group: name, Path: in(list, o.Name), Value: o.Meaning})
					edit(fieldView{Label: o.Name, Group: name, Path: in(list, j, o.Name), Value: o.Meaning})
				}
				if q.YesIf != "" {
					edit(fieldView{Label: "Yes if", Group: name, Path: in("yes_if"), Value: q.YesIf})
					edit(fieldView{Label: "No if", Group: name, Path: in("no_if"), Value: q.NoIf})
				}
			}
		case s.HTTP != nil:
			// The URL is shown as written, with its ${...} unresolved, so no
			// secret it resolves to leaves the server.
			step.Subtitle = strings.ToUpper(s.HTTP.Method) + " " + s.HTTP.URL
			add("request", step.Subtitle)
			if body, ok := s.HTTP.Body.(map[string]any); ok {
				add("sends", strings.Join(slices.Sorted(maps.Keys(body)), ", "))
			}
			edit(fieldView{
				Label: "Method", Path: at("http", "method"), Value: strings.ToUpper(s.HTTP.Method),
				Kind: kindChoice, Options: workflow.Methods,
			})
			edit(fieldView{Label: "URL", Path: at("http", "url"), Value: s.HTTP.URL})
			for _, name := range slices.Sorted(maps.Keys(s.HTTP.Headers)) {
				edit(fieldView{Label: name, Group: "Headers", Path: at("http", "headers", name), Value: s.HTTP.Headers[name]})
			}
			texts("Body", "", at("http", "body"), s.HTTP.Body)
		}
		view.Steps = append(view.Steps, step)
	}
	return view
}

// questions describes what a decide step or component asks, by name.
func questions(d *workflow.DecideStep) []questionView {
	var views []questionView
	for _, name := range slices.Sorted(maps.Keys(d.Questions)) {
		q := d.Questions[name]
		spec := q.Spec()
		views = append(views, questionView{
			Name: name, Ask: q.Ask, Answers: answers(q),
			Options: spec.Options, YesIf: spec.YesIf, NoIf: spec.NoIf,
		})
	}
	return views
}

// shortModel is a model's name without its provider and vendor.
func shortModel(model string) string {
	return model[strings.LastIndex(model, "/")+1:]
}

// answers describes what a question can be answered with.
func answers(q workflow.Question) string {
	spec := q.Spec()
	var names []string
	for _, o := range spec.Options {
		names = append(names, o.Name)
	}
	switch spec.Kind {
	case ai.Choice:
		return "one of " + strings.Join(names, ", ")
	case ai.Score:
		return "score: " + strings.Join(names, " < ")
	}
	return "yes or no"
}
