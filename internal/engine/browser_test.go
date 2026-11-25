package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flovrlabs/flovr/internal/ai"
	"github.com/flovrlabs/flovr/internal/browser"
	"github.com/flovrlabs/flovr/internal/workflow"
)

// chooser is a decision model that uses the first thing on the page whose
// name holds one of its words, in the order they are given.
type chooser struct {
	words []string
	asked []ai.DecideRequest
}

func (c *chooser) Generate(context.Context, ai.Request) (ai.Response, error) {
	return ai.Response{}, nil
}

func (c *chooser) Decide(_ context.Context, req ai.DecideRequest) (ai.DecideResponse, error) {
	c.asked = append(c.asked, req)
	pick := "none"
	for _, word := range c.words {
		for _, o := range req.Questions["use"].Options {
			if pick == "none" && strings.Contains(o.Meaning, word) {
				pick = o.Name
			}
		}
	}
	return ai.DecideResponse{Answers: map[string]any{"use": map[string]any{"value": pick, "confidence": 0.9}}, Model: "chooser", InputTokens: 5}, nil
}

// shop is a site of two pages: a cart, whose button pays and shows a receipt.
func shop(t *testing.T) string {
	t.Helper()
	// CI's machines have a Chrome that does not start without more setting up.
	if !browser.Available() || os.Getenv("CI") != "" {
		t.Skip("no Chrome to drive on this machine")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/cart" {
			fmt.Fprint(w, `<title>Cart</title><h1>Your cart</h1><a href="/">Back</a>
				<button onclick="document.title = 'Receipt'; document.querySelector('h1').textContent = 'Thank you'">Pay now</button>`)
			return
		}
		fmt.Fprint(w, `<title>Shop</title><h1>Shop</h1><a href="/cart">Go to the cart</a> <a href="/help">Help</a>`)
	}))
	t.Cleanup(site.Close)
	return site.URL
}

func browserFlow(t *testing.T, steps string) *workflow.Workflow {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.yaml")
	os.WriteFile(path, []byte("name: shop\ntrigger:\n  manual: true\nsteps:\n"+steps), 0o644)
	w, err := workflow.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestBrowserStepsUseAPageUntilItIsAsWanted(t *testing.T) {
	site := shop(t)
	w := browserFlow(t, `  - id: open
    browser:
      open: ${env.SITE}
  - id: pay
    browser:
      model: openrouter/cloudflare/clef
      do: Pay for what is in the cart
      until: page.title == "Receipt"
      max: 4
`)
	model := &chooser{words: []string{"Pay", "cart"}}
	var results []StepResult
	eng := &Engine{Model: model, Env: map[string]string{"SITE": site}, OnStep: func(r StepResult) { results = append(results, r) }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := eng.Run(ctx, w, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	opened, _ := results[0].Output.(map[string]any)
	paid, _ := results[1].Output.(map[string]any)
	if opened["title"] != "Shop" || results[0].Detail != "opened" {
		t.Errorf("the page opened: %v, %q", opened, results[0].Detail)
	}
	// The second step goes on from the page the first left.
	if paid["title"] != "Receipt" || !strings.Contains(fmt.Sprint(paid["text"]), "Thank you") || fmt.Sprint(paid["used"]) != "[link: Go to the cart button: Pay now]" {
		t.Errorf("after paying: %v", paid)
	}
	if results[1].Detail != "used link: Go to the cart, then button: Pay now · chooser" || results[1].Tokens.In != 10 {
		t.Errorf("detail %q, tokens %+v", results[1].Detail, results[1].Tokens)
	}
	// The model is told what to do and what was used, and offered only what the page holds.
	second := model.asked[1]
	if fmt.Sprint(second.State.(map[string]any)["already used, in order"]) != "[link: Go to the cart]" || len(second.Questions["use"].Options) != 3 {
		t.Errorf("the second question: %+v", second)
	}
}

func TestABrowserStepSaysWhyItStopped(t *testing.T) {
	site := shop(t)
	for name, tt := range map[string]struct{ step, words, want string }{
		"nothing fits":  {"do: Delete the account", "", "found nothing on the page to use for: Delete the account. The page shows: link: Go to the cart; link: Help"},
		"never reached": {"do: Look around\n      until: page.title == \"Receipt\"\n      max: 2", "Help,Go", `used 2 things and the page is still not as until says (page.title == "Receipt")`},
	} {
		t.Run(name, func(t *testing.T) {
			w := browserFlow(t, "  - id: act\n    browser:\n      open: "+site+"\n      model: openrouter/cloudflare/clef\n      "+tt.step+"\n")
			eng := &Engine{Model: &chooser{words: strings.Split(tt.words, ",")}}
			if tt.words == "" {
				eng.Model = &chooser{}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_, err := eng.Run(ctx, w, map[string]any{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one holding %q", err, tt.want)
			}
		})
	}
}
