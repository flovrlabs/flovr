package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/flovrlabs/flovr/internal/yamledit"
)

// triggerRequest has a workflow start another way: on a request to a path,
// or on a schedule.
type triggerRequest struct {
	Workflow string `json:"workflow"`
	Rev      string `json:"rev"`
	Webhook  string `json:"webhook"`
	Cron     string `json:"cron"`
}

// switchTrigger changes what starts a workflow. The key of the other way
// goes, and with a schedule so does the body schema, which only a request
// has. As with every change, nothing is written unless the workflow can
// still be served: one that answers its caller cannot run on a schedule.
func (s *Server) switchTrigger(req triggerRequest) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, w, data, status, refused := s.open(req.Workflow, req.Rev)
	if status != 0 {
		return status, refused
	}
	webhook, cron := strings.TrimSpace(req.Webhook), strings.TrimSpace(req.Cron)
	if (webhook == "") == (cron == "") {
		return http.StatusUnprocessableEntity, fail("Say one thing that starts the workflow: a webhook path or a schedule.")
	}
	// The new key is written first: a mapping cannot lose its only key.
	t := w.Trigger
	var edits []yamledit.Edit
	what := ""
	if webhook != "" {
		edits = append(edits, yamledit.Edit{Path: []any{"trigger", "webhook"}, Value: webhook})
		if t.Cron != "" {
			edits = append(edits, yamledit.Edit{Path: []any{"trigger", "cron"}, Remove: true})
		}
		what = "a request to POST " + webhook
	} else {
		edits = append(edits, yamledit.Edit{Path: []any{"trigger", "cron"}, Value: cron})
		if t.Webhook != "" {
			edits = append(edits, yamledit.Edit{Path: []any{"trigger", "webhook"}, Remove: true})
		}
		if t.Input != "" {
			edits = append(edits, yamledit.Edit{Path: []any{"trigger", "input"}, Remove: true})
		}
		what = "the schedule " + cron
	}
	out, err := yamledit.Apply(data, edits...)
	if err != nil {
		return http.StatusUnprocessableEntity, fail("%v", err)
	}
	status, answer := s.save(i, out, fmt.Sprintf("now started by %s", what))
	// The problems are in the file as it would have been.
	if f, ok := answer.(failure); ok && status != http.StatusOK {
		for j := range f.Problems {
			f.Problems[j].Line = 0
		}
		answer = f
	}
	return status, answer
}
