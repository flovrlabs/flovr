// Package yamledit changes single values in the text of a YAML document and
// leaves every other line as it was written: comments, blank lines, quoting
// and order all survive, so the diff of an edit is as small as the edit.
//
// It works on block-style mappings, the indented "key: value" form. It refuses
// what it cannot change safely, and it checks every result by reading it back.
package yamledit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Edit is one change to a document.
type Edit struct {
	// Path leads to the value: mapping keys as strings and list positions as
	// ints. Its last element must be a mapping key.
	Path []any

	// Value is what to set: a string, bool, number or nil.
	Value any

	// Remove deletes the key and its value instead of setting it.
	Remove bool

	// After names the key that a new key is added behind. A new key goes last
	// in its mapping if After is empty or is not a key of that mapping.
	After string
}

// errInPlace is returned when an edit would have touched more than it should.
var errInPlace = errors.New("cannot be changed in place without disturbing the rest of the file; change it in the file itself")

// Apply makes the edits to src in order and returns the new text.
func Apply(src []byte, edits ...Edit) ([]byte, error) {
	for _, e := range edits {
		next, err := apply(src, e)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pathString(e.Path), err)
		}
		src = next
	}
	return src, nil
}

func apply(src []byte, e Edit) ([]byte, error) {
	if len(e.Path) == 0 {
		return nil, errors.New("the path is empty")
	}
	key, ok := e.Path[len(e.Path)-1].(string)
	if !ok {
		return nil, errors.New("only the value of a key can be changed, not a list item")
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, errors.New("the document is empty")
	}
	parent, err := find(doc.Content[0], e.Path[:len(e.Path)-1])
	if err != nil {
		return nil, err
	}
	switch {
	case parent.Kind != yaml.MappingNode:
		return nil, errors.New("what contains it is not a mapping")
	case parent.Style&yaml.FlowStyle != 0:
		return nil, errors.New("its mapping is written inline as {...}, which " + errInPlace.Error())
	}
	var k, v *yaml.Node
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			k, v = parent.Content[i], parent.Content[i+1]
			break
		}
	}

	lines := split(src)
	switch {
	case e.Remove && k == nil:
		return src, nil
	case e.Remove:
		lines, err = remove(lines, parent, k, v)
	case k == nil:
		lines, err = insert(lines, parent, key, e.Value, e.After)
	default:
		lines, err = replace(lines, k, v, e.Value)
	}
	if err != nil {
		return nil, err
	}
	out := join(lines)
	if err := verify(src, out, e); err != nil {
		return nil, err
	}
	return out, nil
}

// find follows path down from n.
func find(n *yaml.Node, path []any) (*yaml.Node, error) {
	for i, p := range path {
		var next *yaml.Node
		switch p := p.(type) {
		case string:
			if n.Kind == yaml.MappingNode {
				for j := 0; j+1 < len(n.Content); j += 2 {
					if n.Content[j].Value == p {
						next = n.Content[j+1]
						break
					}
				}
			}
		case int:
			if n.Kind == yaml.SequenceNode && p >= 0 && p < len(n.Content) {
				next = n.Content[p]
			}
		default:
			return nil, fmt.Errorf("a path holds keys and list positions, not %T", p)
		}
		if next == nil {
			return nil, fmt.Errorf("there is no %s in the file", pathString(path[:i+1]))
		}
		if next.Kind == yaml.AliasNode {
			return nil, fmt.Errorf("%s is an alias of another value, which %w", pathString(path[:i+1]), errInPlace)
		}
		n = next
	}
	return n, nil
}

// A line is one line of the document and the line ending it was written with.
type line struct{ text, eol string }

func split(src []byte) []line {
	var lines []line
	for len(src) > 0 {
		i := bytes.IndexByte(src, '\n')
		if i < 0 {
			lines = append(lines, line{string(src), ""})
			break
		}
		text, eol := string(src[:i]), "\n"
		if strings.HasSuffix(text, "\r") {
			text, eol = text[:len(text)-1], "\r\n"
		}
		lines = append(lines, line{text, eol})
		src = src[i+1:]
	}
	return lines
}

func join(lines []line) []byte {
	var buf bytes.Buffer
	for _, l := range lines {
		buf.WriteString(l.text)
		buf.WriteString(l.eol)
	}
	return buf.Bytes()
}

func blank(text string) bool { return strings.TrimSpace(text) == "" }

func indent(text string) int { return len(text) - len(strings.TrimLeft(text, " ")) }

// extent returns the lines [start, end) holding the entry with key k and
// value v: the key's line and the more deeply indented lines that follow it.
// Blank lines after the entry are not part of it.
func extent(lines []line, k, v *yaml.Node) (start, end int) {
	start, depth := k.Line-1, k.Column-1
	// A list may be written at the depth of the key it belongs to.
	listAtDepth := v.Kind == yaml.SequenceNode && v.Style&yaml.FlowStyle == 0 && v.Column == k.Column
	end = start + 1
	for ; end < len(lines); end++ {
		text := lines[end].text
		if blank(text) {
			continue
		}
		d := indent(text)
		if d > depth || listAtDepth && d == depth && (text[d:] == "-" || strings.HasPrefix(text[d:], "- ")) {
			continue
		}
		break
	}
	for end > start+1 && blank(lines[end-1].text) {
		end--
	}
	return start, end
}

// replace rewrites the entry of k with value, keeping what comes before the
// key on its line and a comment that ends that line.
func replace(lines []line, k, v *yaml.Node, value any) ([]line, error) {
	start, end := extent(lines, k, v)
	first, depth := lines[start].text, k.Column-1
	if depth > len(first) {
		return nil, errInPlace
	}
	entry, err := render(first[:depth], k.Value, k.Style, value, v.Style, depth)
	if err != nil {
		return nil, err
	}
	entry[0] += trailingComment(first, k, v)
	return splice(lines, start, end, entry), nil
}

// insert adds key with value to the mapping, behind the key named after or
// else behind the mapping's last key.
func insert(lines []line, parent *yaml.Node, key string, value any, after string) ([]line, error) {
	at := len(parent.Content) - 2
	for i := 0; i+1 < len(parent.Content) && after != ""; i += 2 {
		if parent.Content[i].Value == after {
			at = i
			break
		}
	}
	_, end := extent(lines, parent.Content[at], parent.Content[at+1])
	// Every key of a mapping starts in the same column.
	depth := parent.Content[0].Column - 1
	entry, err := render(strings.Repeat(" ", depth), key, 0, value, 0, depth)
	if err != nil {
		return nil, err
	}
	return splice(lines, end, end, entry), nil
}

// remove deletes the entry of k, and the comment lines directly above it,
// which describe it.
func remove(lines []line, parent, k, v *yaml.Node) ([]line, error) {
	if len(parent.Content) == 2 {
		return nil, errors.New("is the only key of its mapping, so it cannot be removed")
	}
	start, end := extent(lines, k, v)
	depth := k.Column - 1
	if depth > len(lines[start].text) || !blank(lines[start].text[:depth]) {
		return nil, errors.New("is the first key of a list item, so it cannot be removed")
	}
	for start > 0 {
		above := lines[start-1].text
		if indent(above) != depth || !strings.HasPrefix(above[depth:], "#") {
			break
		}
		start--
	}
	// Where the entry stood between two blank lines, one of them goes too.
	if start > 0 && blank(lines[start-1].text) && end < len(lines) && blank(lines[end].text) {
		end++
	}
	return splice(lines, start, end, nil), nil
}

// splice replaces lines[start:end] with entry, giving the new lines the line
// ending used around them.
func splice(lines []line, start, end int, entry []string) []line {
	eol := "\n"
	for _, l := range lines {
		if l.eol != "" {
			eol = l.eol
			break
		}
	}
	out := append([]line{}, lines[:start]...)
	for _, text := range entry {
		out = append(out, line{text, eol})
	}
	out = append(out, lines[end:]...)
	// A file that did not end with a line break still does not.
	if n := len(lines); n > 0 && lines[n-1].eol == "" && len(out) > 0 {
		for i := range out[:len(out)-1] {
			if out[i].eol == "" {
				out[i].eol = eol
			}
		}
		out[len(out)-1].eol = ""
	}
	return out
}

// render returns the lines of the entry "key: value". prefix is what stands
// before the key on its line, and depth the column the key starts in.
func render(prefix, key string, keyStyle yaml.Style, value any, was yaml.Style, depth int) ([]string, error) {
	head := prefix + scalar(key, keyStyle&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle)) + ":"

	text, isText := value.(string)
	switch {
	case !isText:
		switch value.(type) {
		case nil, bool, int, int64, float64:
		default:
			return nil, fmt.Errorf("a value of type %T cannot be written", value)
		}
		out, err := yaml.Marshal(value)
		if err != nil {
			return nil, err
		}
		return []string{head + " " + strings.TrimSpace(string(out))}, nil

	case strings.Contains(text, "\n") && blockable(text):
		// Text of several lines is written as a block, one line per line.
		body := strings.TrimRight(text, "\n")
		mark := "|-"
		switch len(text) - len(body) {
		case 0:
		case 1:
			mark = "|"
		default:
			mark, body = "|+", text[:len(text)-1]
		}
		entry := []string{head + " " + mark}
		for _, l := range strings.Split(body, "\n") {
			if l != "" {
				l = strings.Repeat(" ", depth+2) + l
			}
			entry = append(entry, l)
		}
		return entry, nil
	}
	// A value keeps the quotes it was written with.
	return []string{head + " " + scalar(text, was&(yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle))}, nil
}

// blockable reports whether text can be written as a literal block, which
// holds neither a first line that starts with a space nor control characters.
func blockable(text string) bool {
	if strings.HasPrefix(strings.TrimLeft(text, "\n"), " ") {
		return false
	}
	for _, r := range text {
		if r < ' ' && r != '\n' || r == 0x7f || r == 0x85 || r == 0x2028 || r == 0x2029 || r == 0xfeff {
			return false
		}
	}
	return true
}

// scalar writes text on one line, quoted only if YAML would otherwise read it
// as something other than this text.
func scalar(text string, style yaml.Style) string {
	out, err := yaml.Marshal(&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: text, Style: style})
	written := strings.TrimSuffix(string(out), "\n")
	if err == nil && !strings.Contains(written, "\n") {
		return written
	}
	// A JSON string is also a YAML string, and is always one line.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(text)
	return strings.TrimSuffix(buf.String(), "\n")
}

// trailingComment returns the comment that ends the line, with the space
// before it, if it is the line comment of one of the nodes.
func trailingComment(text string, nodes ...*yaml.Node) string {
	for _, n := range nodes {
		c := n.LineComment
		if c == "" {
			continue
		}
		if i := strings.LastIndex(text, c); i >= 0 && i+len(c) == len(strings.TrimRight(text, " \t")) {
			return text[len(strings.TrimRight(text[:i], " \t")):]
		}
	}
	return ""
}

// verify reads the edited document back and checks that it differs from the
// original in the edited value alone, and that no comment was lost.
func verify(before, after []byte, e Edit) error {
	var was, is yaml.Node
	var want, got any
	if err := yaml.Unmarshal(before, &was); err != nil {
		return err
	}
	if err := yaml.Unmarshal(after, &is); err != nil {
		return errInPlace
	}
	if was.Decode(&want) != nil || is.Decode(&got) != nil {
		return errInPlace
	}
	want, ok := change(plain(want), e.Path, e.Value, e.Remove)
	a, errA := json.Marshal(want)
	b, errB := json.Marshal(plain(got))
	if !ok || errA != nil || errB != nil || !bytes.Equal(a, b) {
		return errInPlace
	}

	if !e.Remove {
		had, has := map[string]int{}, map[string]int{}
		comments(&was, had)
		comments(&is, has)
		for c, n := range had {
			if has[c] < n {
				return errInPlace
			}
		}
	}
	return nil
}

// plain turns the maps a YAML decoder produces into ones with string keys.
func plain(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			v[k] = plain(child)
		}
	case map[any]any:
		m := make(map[string]any, len(v))
		for k, child := range v {
			m[fmt.Sprint(k)] = plain(child)
		}
		return m
	case []any:
		for i, child := range v {
			v[i] = plain(child)
		}
	}
	return v
}

// change makes the edit to a decoded document. It reports false if the path
// does not lead through it.
func change(root any, path []any, value any, remove bool) (any, bool) {
	at := root
	for _, p := range path[:len(path)-1] {
		switch p := p.(type) {
		case string:
			m, ok := at.(map[string]any)
			if !ok {
				return nil, false
			}
			at = m[p]
		case int:
			l, ok := at.([]any)
			if !ok || p < 0 || p >= len(l) {
				return nil, false
			}
			at = l[p]
		}
	}
	m, ok := at.(map[string]any)
	if !ok {
		return nil, false
	}
	key := path[len(path)-1].(string)
	if remove {
		delete(m, key)
	} else {
		m[key] = value
	}
	return root, true
}

// comments counts the comment lines of n and everything in it.
func comments(n *yaml.Node, count map[string]int) {
	for _, c := range []string{n.HeadComment, n.LineComment, n.FootComment} {
		for _, l := range strings.Split(c, "\n") {
			if l != "" {
				count[l]++
			}
		}
	}
	for _, child := range n.Content {
		comments(child, count)
	}
}

// pathString writes a path the way an expression would, as steps[1].when.
func pathString(path []any) string {
	var sb strings.Builder
	for _, p := range path {
		switch p := p.(type) {
		case int:
			fmt.Fprintf(&sb, "[%d]", p)
		default:
			if sb.Len() > 0 {
				sb.WriteByte('.')
			}
			fmt.Fprint(&sb, p)
		}
	}
	return sb.String()
}
