package engine

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/browser"
	"github.com/flovrlabs/flovr/internal/expr"
	"github.com/flovrlabs/flovr/internal/workflow"
)

// pages holds the browser of a run: started by the first browser step that
// needs it, shared by the steps after, and closed when the run ends.
type pages struct {
	mu      sync.Mutex
	session *browser.Session
}

func (p *pages) get(ctx context.Context) (*browser.Session, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session == nil {
		s, err := browser.Open(ctx)
		if err != nil {
			return nil, err
		}
		p.session = s
	}
	return p.session, nil
}

func (p *pages) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session != nil {
		p.session.Close()
		p.session = nil
	}
}

// withPages gives the engine a browser to share for one run, and the way to
// close it after. An engine that has one already, as inside a component, keeps it.
func (e *Engine) withPages() (*Engine, func()) {
	if e.pages != nil {
		return e, func() {}
	}
	own := *e
	own.pages = &pages{}
	return &own, own.pages.close
}

// browser opens a page, or uses what is on it towards what the step is to
// do. A decision model picks one thing at a time from a list of what the page
// holds; it writes nothing, so it can only choose what is there.
func (e *Engine) browser(ctx context.Context, s *workflow.BrowserStep, env *expr.Env, vars map[string]any) (any, string, error) {
	open, err := env.EvalString(s.Open, vars)
	if err != nil {
		return nil, "", fmt.Errorf("open: %w", err)
	}
	goal, err := env.EvalString(s.Do, vars)
	if err != nil {
		return nil, "", fmt.Errorf("do: %w", err)
	}
	session, err := e.pages.get(ctx)
	if err != nil {
		return nil, "", err
	}
	if open != "" {
		if err := session.Go(ctx, open); err != nil {
			return nil, "", fmt.Errorf("open: %w", err)
		}
	}

	var cond *expr.Env
	if s.Until != "" {
		if cond, err = expr.Vars("page"); err != nil {
			return nil, "", err
		}
	}
	// reached says whether the page is as until wants it.
	reached := func(p browser.Page) (bool, error) {
		if cond == nil {
			return false, nil
		}
		ok, err := cond.EvalCondition(s.Until, map[string]any{"env": e.Env, "page": map[string]any{"url": p.URL, "title": p.Title, "text": p.Text}})
		if err != nil {
			return false, fmt.Errorf("until: %w", err)
		}
		return ok, nil
	}
	most := 1
	if s.Until != "" {
		most = s.Max
		if most == 0 {
			most = workflow.DefaultActions
		}
	}

	used := []any{}
	page, err := session.Look(ctx)
	if err != nil {
		return nil, "", err
	}
	model := ""
	for goal != "" {
		done, err := reached(page)
		if err != nil {
			return nil, "", err
		}
		if done {
			break
		}
		if len(used) == most {
			if s.Until == "" {
				break
			}
			return nil, "", fmt.Errorf("used %s and the page is still not as until says (%s). It used: %s", count(len(used), "thing"), s.Until, said(used))
		}
		if len(page.Elements) == 0 {
			return nil, "", errors.New("the page has nothing to use: no button, link or box is on the screen")
		}
		options := []ai.Option{{Name: "none", Meaning: "Nothing on this page helps to do it"}}
		for _, el := range page.Elements {
			options = append(options, ai.Option{Name: "e" + strconv.Itoa(el.N), Meaning: el.Role + ": " + el.Name})
		}
		state := map[string]any{"to do": goal, "page": page.Title, "already used, in order": "nothing yet"}
		if len(used) > 0 {
			state["already used, in order"] = used
		}
		resp, err := e.Model.Decide(ctx, ai.DecideRequest{Model: s.Model, State: state, Questions: map[string]ai.Question{"use": {
			Ask:     "A person is on a web page and has something to do. Which one thing on the page should they use next? Do not use again what was already used, unless nothing else helps.",
			Kind:    ai.Choice,
			Options: options,
		}}})
		if err != nil {
			return nil, "", err
		}
		model = resp.Model
		answer, _ := resp.Answers["use"].(map[string]any)
		pick, _ := answer["value"].(string)
		n, _ := strconv.Atoi(strings.TrimPrefix(pick, "e"))
		if pick == "none" || n < 1 || n > len(page.Elements) {
			return nil, "", fmt.Errorf("found nothing on the page to use for: %s. The page shows: %s", goal, listed(page))
		}
		el := page.Elements[n-1]
		if err := session.Click(ctx, n); err != nil {
			return nil, "", fmt.Errorf("could not use %s: %s: %w", el.Role, el.Name, err)
		}
		used = append(used, el.Role+": "+el.Name)
		if page, err = session.Look(ctx); err != nil {
			return nil, "", err
		}
	}

	detail := "opened"
	if goal != "" {
		detail = "used " + said(used)
		if model != "" {
			detail += " · " + model
		}
	}
	return map[string]any{"url": page.URL, "title": page.Title, "text": page.Text, "used": used}, detail, nil
}

func count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// said lists what a step used, for a person to read.
func said(used []any) string {
	if len(used) == 0 {
		return "nothing"
	}
	parts := make([]string, len(used))
	for i, u := range used {
		parts[i] = fmt.Sprint(u)
	}
	return strings.Join(parts, ", then ")
}

// listed is a page's first few things to use, to say what was there.
func listed(p browser.Page) string {
	var names []string
	for i, el := range p.Elements {
		if i == 8 {
			names = append(names, "and "+strconv.Itoa(len(p.Elements)-8)+" more")
			break
		}
		names = append(names, el.Role+": "+el.Name)
	}
	return strings.Join(names, "; ")
}
