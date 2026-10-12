package server

import (
	"crypto/subtle"
	"embed"
	"maps"
	"net/http"
	"slices"
	"strings"

	"aia/internal/ai"
	"aia/internal/workflow"
)

//go:embed ui
var ui embed.FS

// page serves the read-only web page and the JSON it shows.
func (s *Server) page(rw http.ResponseWriter, r *http.Request) {
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
	Workflows []workflowView `json:"workflows"`
	Runs      []Run          `json:"runs"` // newest first, without triggers and outputs
}

type workflowView struct {
	Name    string     `json:"name"`
	Path    string     `json:"path"`
	Trigger string     `json:"trigger"` // such as "POST /tickets"; empty if it is not served
	Tests   int        `json:"tests"`
	Steps   []stepView `json:"steps"`
}

type stepView struct {
	ID        string         `json:"id"`
	Kind      string         `json:"kind"`
	Subtitle  string         `json:"subtitle"` // a few words for the step's box
	When      string         `json:"when,omitempty"`
	Reads     []string       `json:"reads"`  // the trigger and earlier steps it uses
	Fields    [][2]string    `json:"fields"` // label and value pairs describing it
	Questions []questionView `json:"questions,omitempty"`
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
	view := stateView{
		Commit: s.opts.Commit, Protected: s.opts.Token != "",
		Workflows: []workflowView{}, Runs: s.history.recent(),
	}
	for i := range view.Runs {
		r := &view.Runs[i]
		r.Trigger = nil
		r.Steps = slices.Clone(r.Steps)
		for j := range r.Steps {
			r.Steps[j].Output = nil
		}
	}
	for _, w := range s.workflows {
		view.Workflows = append(view.Workflows, describe(w))
	}
	return view
}

func describe(w *workflow.Workflow) workflowView {
	view := workflowView{Name: w.Name, Path: w.Path, Tests: len(w.Tests), Steps: []stepView{}}
	switch t := w.Trigger; {
	case t != nil && t.Webhook != "":
		view.Trigger = "POST " + t.Webhook
	case t != nil && t.Cron != "":
		view.Trigger = "cron " + t.Cron
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
		switch {
		case s.AI != nil:
			step.Subtitle = shortModel(s.AI.Model)
			add("model", s.AI.Model)
			add("prompt", s.AI.Prompt)
			add("output", s.AI.Output)
		case s.Decide != nil:
			step.Subtitle = shortModel(s.Decide.Model)
			add("model", s.Decide.Model)
			for _, name := range slices.Sorted(maps.Keys(s.Decide.Questions)) {
				q := s.Decide.Questions[name]
				spec := q.Spec()
				step.Questions = append(step.Questions, questionView{
					Name: name, Ask: q.Ask, Answers: answers(q),
					Options: spec.Options, YesIf: spec.YesIf, NoIf: spec.NoIf,
				})
			}
		case s.HTTP != nil:
			// The URL is shown as written, with its ${...} unresolved, so no
			// secret it resolves to leaves the server.
			step.Subtitle = strings.ToUpper(s.HTTP.Method) + " " + s.HTTP.URL
			add("request", step.Subtitle)
			if body, ok := s.HTTP.Body.(map[string]any); ok {
				add("sends", strings.Join(slices.Sorted(maps.Keys(body)), ", "))
			}
		}
		view.Steps = append(view.Steps, step)
	}
	return view
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
