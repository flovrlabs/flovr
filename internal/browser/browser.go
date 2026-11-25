// Package browser drives a Chrome of this machine for browser steps: it opens
// pages, reads a page as a table of what can be used on it, and clicks.
package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Session is one Chrome with one page, started for a run and closed after it.
type Session struct {
	cmd  *exec.Cmd
	dir  string // its profile, removed when it closes
	conn *websocket.Conn

	// For a person to follow what the steps do: show opens the browser in a
	// window and waits a moment before each thing is used, and shots is a
	// folder that gets a picture of the page each time.
	show  bool
	shots string
	shot  int

	mu      sync.Mutex
	next    int
	waiting map[int]chan reply
	done    chan struct{}
}

type reply struct {
	Result json.RawMessage
	Error  *struct {
		Message string `json:"message"`
	}
}

// chrome finds the browser to start: the one CHROME names, or one where
// Chrome and its kin are usually installed.
func chrome() (string, error) {
	if path := os.Getenv("CHROME"); path != "" {
		return path, nil
	}
	var known []string
	switch runtime.GOOS {
	case "darwin":
		known = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	case "windows":
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			if root != "" {
				known = append(known, filepath.Join(root, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
	}
	for _, path := range known {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("a browser step needs Chrome or Chromium, and none was found; install one, or set CHROME to its path")
}

// Available reports whether a browser to drive is on this machine.
func Available() bool {
	_, err := chrome()
	return err == nil
}

// Open starts a Chrome without a window, with a profile of its own.
func Open(ctx context.Context) (*Session, error) {
	path, err := chrome()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "flovr-browser-")
	if err != nil {
		return nil, err
	}
	// FLOVR_BROWSER_SHOW opens the browser in a window, to watch the steps;
	// FLOVR_BROWSER_SHOTS names a folder for a picture of each thing used.
	show, shots := os.Getenv("FLOVR_BROWSER_SHOW") != "", os.Getenv("FLOVR_BROWSER_SHOTS")
	// Port 0 lets Chrome pick a free port, which it writes to a file.
	args := []string{"--remote-debugging-port=0", "--user-data-dir=" + dir,
		"--no-first-run", "--no-default-browser-check", "--hide-scrollbars", "--window-size=1440,900", "about:blank"}
	if !show {
		args = append([]string{"--headless=new", "--disable-gpu"}, args...)
	}
	cmd := exec.Command(path, args...)
	if err := cmd.Start(); err != nil {
		os.RemoveAll(dir)
		return nil, fmt.Errorf("could not start %s: %w", path, err)
	}
	s := &Session{cmd: cmd, dir: dir, show: show, shots: shots, waiting: map[int]chan reply{}, done: make(chan struct{})}

	var target string
	deadline := time.Now().Add(15 * time.Second)
	for target == "" && time.Now().Before(deadline) && ctx.Err() == nil {
		time.Sleep(100 * time.Millisecond)
		data, err := os.ReadFile(filepath.Join(dir, "DevToolsActivePort"))
		if err != nil {
			continue
		}
		port, _, _ := strings.Cut(string(data), "\n")
		resp, err := http.Get("http://127.0.0.1:" + strings.TrimSpace(port) + "/json/list")
		if err != nil {
			continue
		}
		var pages []struct {
			Type string `json:"type"`
			URL  string `json:"webSocketDebuggerUrl"`
		}
		json.NewDecoder(resp.Body).Decode(&pages)
		resp.Body.Close()
		for _, p := range pages {
			if p.Type == "page" {
				target = p.URL
			}
		}
	}
	if target == "" {
		s.Close()
		return nil, fmt.Errorf("%s did not start in time", path)
	}
	conn, _, err := websocket.Dial(ctx, target, nil)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("could not reach the browser: %w", err)
	}
	conn.SetReadLimit(64 << 20)
	s.conn = conn
	go s.read()
	for _, method := range []string{"Page.enable", "Runtime.enable"} {
		if _, err := s.call(ctx, method, nil); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}

// read hands each answer of the browser to the call that waits for it.
func (s *Session) read() {
	defer close(s.done)
	for {
		_, data, err := s.conn.Read(context.Background())
		if err != nil {
			return
		}
		var msg struct {
			ID int `json:"id"`
			reply
		}
		if json.Unmarshal(data, &msg) != nil || msg.ID == 0 {
			continue
		}
		s.mu.Lock()
		ch := s.waiting[msg.ID]
		delete(s.waiting, msg.ID)
		s.mu.Unlock()
		if ch != nil {
			ch <- msg.reply
		}
	}
}

func (s *Session) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	s.next++
	id := s.next
	ch := make(chan reply, 1)
	s.waiting[id] = ch
	s.mu.Unlock()
	data, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	if err := s.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return nil, fmt.Errorf("the browser is gone: %w", err)
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return nil, errors.New(r.Error.Message)
		}
		return r.Result, nil
	case <-s.done:
		return nil, errors.New("the browser closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// eval runs an expression on the page and decodes what it gives into out.
func (s *Session) eval(ctx context.Context, expr string, out any) error {
	raw, err := s.call(ctx, "Runtime.evaluate", map[string]any{"expression": expr, "awaitPromise": true, "returnByValue": true})
	if err != nil {
		return err
	}
	var got struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text      string `json:"text"`
			Exception struct {
				Description string `json:"description"`
			} `json:"exception"`
		} `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		return err
	}
	if d := got.ExceptionDetails; d != nil {
		return errors.New(strings.TrimSpace(d.Text + " " + d.Exception.Description))
	}
	if out == nil || len(got.Result.Value) == 0 {
		return nil
	}
	return json.Unmarshal(got.Result.Value, out)
}

// settle waits until the page has stopped changing for a moment, or a second
// has passed: a click often loads or redraws something.
func (s *Session) settle(ctx context.Context) {
	last, same := "", 0
	for i := 0; i < 20 && same < 3 && ctx.Err() == nil; i++ {
		time.Sleep(50 * time.Millisecond)
		var now string
		if s.eval(ctx, `document.readyState + "|" + location.href + "|" + document.body.innerText.length + "|" + document.querySelectorAll("*").length`, &now) != nil {
			continue
		}
		if now == last && strings.HasPrefix(now, "complete") {
			same++
		} else {
			last, same = now, 0
		}
	}
}

// Go opens the page at url.
func (s *Session) Go(ctx context.Context, url string) error {
	raw, err := s.call(ctx, "Page.navigate", map[string]any{"url": url})
	if err != nil {
		return err
	}
	var got struct {
		ErrorText string `json:"errorText"`
	}
	if json.Unmarshal(raw, &got); got.ErrorText != "" {
		return fmt.Errorf("could not open the page: %s", got.ErrorText)
	}
	s.settle(ctx)
	return nil
}

// Element is something on a page that can be used: a button, a link, a box.
type Element struct {
	N    int    `json:"n"` // counted from 1 in the order of the page
	Role string `json:"role"`
	Name string `json:"name"`
}

// Page is a page as a browser step sees it.
type Page struct {
	URL      string    `json:"url"`
	Title    string    `json:"title"`
	Text     string    `json:"text"` // what it shows, as text
	Elements []Element `json:"elements"`
}

// look reads the page into a Page, and keeps the elements it listed so that
// Click can use the one a number names. It lists what a person could use now:
// what is on the screen, not covered, hidden or switched off.
const look = `(() => {
  const els = [], rows = [];
  for (const e of document.querySelectorAll("a[href], button, input, select, textarea, summary, [role=button], [role=link], [role=tab], [role=checkbox], [role=menuitem], [role=option]")) {
    const r = e.getBoundingClientRect();
    if (r.width < 2 || r.height < 2 || r.bottom < 0 || r.top > innerHeight || r.right < 0 || r.left > innerWidth) continue;
    if (e.disabled || e.getAttribute("aria-disabled") === "true" || e.closest("[hidden], [inert]") || getComputedStyle(e).visibility === "hidden") continue;
    const role = e.getAttribute("role") || { A: "link", BUTTON: "button", SELECT: "select", TEXTAREA: "textbox", SUMMARY: "toggle" }[e.tagName] || (e.type === "checkbox" ? "checkbox" : e.type === "radio" ? "radio" : e.type === "submit" || e.type === "button" ? "button" : "textbox");
    const label = e.id ? document.querySelector('label[for="' + CSS.escape(e.id) + '"]') : e.closest("label");
    let name = e.getAttribute("aria-label") || (label && label.innerText) || e.innerText || e.value && role === "button" && e.value || e.getAttribute("placeholder") || e.getAttribute("title") || e.getAttribute("name") || "";
    name = name.replace(/\s+/g, " ").trim().slice(0, 100);
    if (e.getAttribute("aria-selected") === "true" || e.getAttribute("aria-pressed") === "true" || e.checked) name += " (on)";
    els.push(e);
    rows.push({ n: els.length, role, name });
  }
  window.__flovr = els;
  return { url: location.href, title: document.title, text: document.body.innerText.replace(/[ \t]+/g, " ").replace(/\n\s*\n+/g, "\n").trim().slice(0, 4000), elements: rows };
})()`

// Look reads the page as it is now.
func (s *Session) Look(ctx context.Context) (Page, error) {
	var p Page
	err := s.eval(ctx, look, &p)
	return p, err
}

// Click uses the element that the last Look numbered n.
func (s *Session) Click(ctx context.Context, n int) error {
	var ok bool
	expr := fmt.Sprintf(`(() => { const e = (window.__flovr || [])[%d]; if (!e || !e.isConnected) return false; e.scrollIntoView({ block: "nearest", inline: "nearest" }); e.focus && e.focus(); e.click(); return true; })()`, n-1)
	if err := s.eval(ctx, expr, &ok); err != nil {
		return err
	}
	if !ok {
		return errors.New("the element is no longer on the page")
	}
	s.settle(ctx)
	return nil
}

// Watched reports whether someone follows what the steps do, in a window or
// in pictures.
func (s *Session) Watched() bool { return s.show || s.shots != "" }

// Point shows a person what is about to be used: it marks the element that
// the last Look numbered n, or none for n of 0, and says caption at the foot
// of the page. The mark is gone once the page is used.
func (s *Session) Point(ctx context.Context, n int, caption string) {
	if !s.Watched() {
		return
	}
	text, _ := json.Marshal(caption)
	s.eval(ctx, fmt.Sprintf(`(() => {
  document.querySelectorAll("[data-flovr-mark]").forEach((m) => m.remove());
  const add = (css, text) => { const d = document.createElement("div"); d.dataset.flovrMark = "1"; d.textContent = text || ""; Object.assign(d.style, css); document.documentElement.appendChild(d); };
  const e = (window.__flovr || [])[%d];
  if (e) {
    e.scrollIntoView({ block: "nearest", inline: "nearest" });
    const r = e.getBoundingClientRect();
    add({ position: "fixed", left: r.left - 5 + "px", top: r.top - 5 + "px", width: r.width + 10 + "px", height: r.height + 10 + "px", boxSizing: "border-box", border: "3px solid #f43f5e", borderRadius: "10px", boxShadow: "0 0 0 4px rgba(244,63,94,0.3)", zIndex: "2147483647", pointerEvents: "none" });
  }
  add({ position: "fixed", left: "0", right: "0", bottom: "0", padding: "12px 18px", background: "#111827", color: "#ffffff", font: "500 15px/1.4 system-ui, sans-serif", zIndex: "2147483647", pointerEvents: "none", whiteSpace: "pre-wrap" }, %s);
})()`, n-1, text), nil)
	if s.shots != "" {
		if raw, err := s.call(ctx, "Page.captureScreenshot", map[string]any{"format": "png"}); err == nil {
			var got struct {
				Data string `json:"data"`
			}
			if json.Unmarshal(raw, &got) == nil {
				if png, err := base64.StdEncoding.DecodeString(got.Data); err == nil {
					s.shot++
					os.MkdirAll(s.shots, 0o755)
					os.WriteFile(filepath.Join(s.shots, fmt.Sprintf("%03d.png", s.shot)), png, 0o644)
				}
			}
		}
	}
	if s.show {
		time.Sleep(1200 * time.Millisecond)
	}
	s.eval(ctx, `document.querySelectorAll("[data-flovr-mark]").forEach((m) => m.remove())`, nil)
}

// Close ends the browser and removes its profile.
func (s *Session) Close() {
	if s.conn != nil {
		s.conn.CloseNow()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
	os.RemoveAll(s.dir)
}
