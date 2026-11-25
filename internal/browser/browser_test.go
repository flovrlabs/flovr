package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAPageIsReadAndClicked(t *testing.T) {
	// CI's machines have a Chrome that does not start without more setting up.
	if !Available() || os.Getenv("CI") != "" {
		t.Skip("no Chrome to drive on this machine")
	}
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<title>Shop</title><h1>Cart</h1>
			<button onclick="document.querySelector('h1').textContent = 'Paid'">Pay now</button>
			<a href="/help">Help</a> <button hidden>Secret</button> <button disabled>Not yet</button>
			<label>Email <input type="text"></label>`)
	}))
	t.Cleanup(site.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	s, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Go(ctx, site.URL); err != nil {
		t.Fatal(err)
	}
	page, err := s.Look(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for _, e := range page.Elements {
		rows = append(rows, e.Role+": "+e.Name)
	}
	// What is hidden or switched off cannot be used, so it is not listed.
	if got := strings.Join(rows, " | "); got != "button: Pay now | link: Help | textbox: Email" || page.Title != "Shop" || !strings.Contains(page.Text, "Cart") {
		t.Fatalf("page = %+v", page)
	}
	if err := s.Click(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if page, _ = s.Look(ctx); !strings.Contains(page.Text, "Paid") {
		t.Errorf("after the click the page shows %q", page.Text)
	}
	if err := s.Click(ctx, 9); err == nil {
		t.Error("an element that is not there was clicked")
	}
}
