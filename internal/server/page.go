package server

import (
	"crypto/subtle"
	"embed"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"

	"github.com/simonyosb/flowmaker/internal/ai"
	"github.com/simonyosb/flowmaker/internal/workflow"
)

//go:embed ui
var ui embed.FS

// page serves the web page and the JSON it shows and, if the server allows
// editing, takes the changes the page makes.
func (s *Server) page(rw http.ResponseWriter, r *http.Request) {
	if s.opts.Edit && r.Method == http.MethodPost && slices.Contains([]string{"/api/edit", "/api/steps", "/api/prompt", "/api/test", "/api/cases", "/api/workflows"}, r.URL.Path) {
		s.change(rw, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		reply(rw, http.StatusNotFound, map[string]string{"error": "no workflow has this webhook path"})
		return
	}
	if !s.mayView(r) {
		rw.Header().Set("WWW-Authenticate", `Basic realm="flowmaker", charset="UTF-8"`)
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
	Commit    string `json:"commit"`
	Protected bool   `json:"protected"` // whether requests need the token
	Editable  bool   `json:"editable"`  // whether the page may change the files
	// Roots are the folders a new workflow can be made in, if the page may
	// make one.
	Roots     []string       `json:"roots,omitempty"`
	Workflows []workflowView `json:"workflows"`
	Runs      []Run          `json:"runs"` // newest first, without triggers and outputs
}

type workflowView struct {
	// Name identifies the workflow among everything the page shows. For a
	// component, which the page shows in the form of a workflow, it is the
	// path of the component's folder, and Title is the component's name.
	Name    string     `json:"name"`
	Title   string     `json:"title,omitempty"`
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

	// Cases are the workflow's tests, which the page can start.
	Cases []caseView `json:"cases,omitempty"`

	// For a component. What it takes stands where a workflow has its
	// trigger, and what it gives back where a workflow has its answer. A
	// component that does one kind of work has that as its only step, under
	// the component's name.
	Component   bool        `json:"component,omitempty"`
	Description string      `json:"description,omitempty"`
	Inputs      []inputView `json:"inputs,omitempty"`
	Gives       *givesView  `json:"gives,omitempty"`  // of a component made of steps
	UsedBy      []useView   `json:"usedBy,omitempty"` // the steps that use it
	Fixed       bool        `json:"fixed,omitempty"`  // steps are not added or removed from the page
}

// givesView is what a component made of steps gives back, as written.
type givesView struct {
	Body   any         `json:"body"`           // with its ${...} unresolved
	Fields []string    `json:"fields"`         // what a step that uses the component can read from it
	Reads  []string    `json:"reads"`          // the input and steps it uses
	Edit   []fieldView `json:"edit,omitempty"` // only when the page may change the files
}

// useView is a step that uses a component.
type useView struct {
	Workflow string `json:"workflow"` // the name the page knows the workflow or component by
	Step     string `json:"step"`
}

// caseView is one of a workflow's or a component's tests.
type caseView struct {
	Name    string       `json:"name"`
	Fixture string       `json:"fixture,omitempty"` // of a workflow's test
	Input   any          `json:"input,omitempty"`   // of a component's test: the values, or the file that holds them
	Expect  []expectView `json:"expect"`            // ordered by key
}

type expectView struct {
	Key  string `json:"key"`
	Want any    `json:"want"`
}

// cases describes the tests of a workflow.
func cases(w *workflow.Workflow) []caseView {
	var views []caseView
	for _, t := range w.Tests {
		view := caseView{Name: t.Name, Fixture: t.Fixture, Expect: []expectView{}}
		if view.Name == "" {
			view.Name = t.Fixture
		}
		for _, key := range slices.Sorted(maps.Keys(t.Expect)) {
			view.Expect = append(view.Expect, expectView{key, t.Expect[key]})
		}
		views = append(views, view)
	}
	return views
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
	// as the step gives it, and for a component made of steps, those steps.
	Component string      `json:"component,omitempty"`
	Uses      string      `json:"uses,omitempty"`
	Target    string      `json:"target,omitempty"` // the name the page knows that component by
	Inner     []innerView `json:"inner,omitempty"`

	// Outputs lists what later steps can read from this one, as the fields
	// to put behind its id, such as "category.value". It is absent where
	// nothing is known of what the step puts out.
	Outputs []string `json:"outputs,omitempty"`
}

// innerView is a step of a component made of steps.
type innerView struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Subtitle string `json:"subtitle"`
	When     string `json:"when,omitempty"`
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
		r.Trigger, r.Response, r.Gave = nil, nil, nil
		r.Steps = withoutOutputs(r.Steps)
	}
	components, uses := s.components()
	if s.opts.Edit {
		for _, root := range s.opts.Roots {
			view.Roots = append(view.Roots, filepath.ToSlash(root))
		}
	}
	for _, w := range s.live.Load().workflows {
		view.Workflows = append(view.Workflows, s.viewAmong(w, components))
	}
	// Components follow, each in the form of a workflow.
	for _, id := range sortedIDs(components) {
		view.Workflows = append(view.Workflows, s.componentView(components[id], uses[id]))
	}
	return view
}

// view describes w for the page. If the server allows editing, the caller
// holds s.mu.
func (s *Server) view(w *workflow.Workflow) workflowView {
	if s.sources[w.Path] == nil {
		return s.viewAmong(w, nil)
	}
	components, _ := s.components()
	return s.viewAmong(w, components)
}

// viewAmong is view for a caller that has the components the page knows.
func (s *Server) viewAmong(w *workflow.Workflow, components map[string]*workflow.Component) workflowView {
	view := describe(w)
	src := s.sources[w.Path]
	if src == nil {
		// Nothing can be changed, so the page is not told what could be.
		stripEdits(&view)
		return view
	}
	view.Rev, view.Problem = src.rev, src.problem
	view.Components, view.Cases = catalogue(w, components), cases(w)
	return view
}

// subtitle is a few words on what a step does, for where it is listed.
func subtitle(s workflow.Step) string {
	switch c := s.Component(); {
	case c != nil:
		return c.Name
	case s.AI != nil:
		return shortModel(s.AI.Model)
	case s.Decide != nil:
		return shortModel(s.Decide.Model)
	case s.HTTP != nil:
		return strings.ToUpper(s.HTTP.Method) + " " + s.HTTP.URL
	}
	return ""
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
