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
	"slices"
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
	// Where the entry stood between two blank lines, one of them goes too,
	// and where it ended the file, so do the blank lines that set it apart.
	if start > 0 && blank(lines[start-1].text) && end < len(lines) && blank(lines[end].text) {
		end++
	}
	for end == len(lines) && start > 0 && blank(lines[start-1].text) {
		start--
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
	return check(before, after, func(root any) (any, bool) {
		return change(root, e.Path, e.Value, e.Remove)
	}, !e.Remove)
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

// InsertItem returns src with a new item in the list at path. The item is
// given as the text of its mapping, written from the first column, and
// becomes the item at position at, counted from 0; the items from there on
// move down. Items of a list that are set apart by blank lines stay so.
func InsertItem(src []byte, path []any, at int, item string) ([]byte, error) {
	out, err := insertItem(src, path, at, item)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", pathString(path), err)
	}
	return out, nil
}

// RemoveItem returns src without the item at position at of the list at path,
// and without the comment lines directly above it, which describe it.
func RemoveItem(src []byte, path []any, at int) ([]byte, error) {
	out, err := removeItem(src, path, at)
	if err != nil {
		return nil, fmt.Errorf("%s[%d]: %w", pathString(path), at, err)
	}
	return out, nil
}

// list finds the list at path and checks that its items can be told apart by
// line: it is written in indented form, one item starting per "- ".
func list(src []byte, path []any) (*yaml.Node, []line, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil, errors.New("the document is empty")
	}
	seq, err := find(doc.Content[0], path)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case seq.Kind != yaml.SequenceNode:
		return nil, nil, errors.New("is not a list")
	case seq.Style&yaml.FlowStyle != 0:
		return nil, nil, errors.New("is written inline as [...], which " + errInPlace.Error())
	case len(seq.Content) == 0:
		return nil, nil, errors.New("is empty, which " + errInPlace.Error())
	}
	lines := split(src)
	for _, it := range seq.Content {
		if _, ok := dash(lines, it); !ok {
			return nil, nil, errInPlace
		}
	}
	return seq, lines, nil
}

// dash returns the column of the "- " that starts the list item it, which
// must stand on the line the item's content starts on.
func dash(lines []line, it *yaml.Node) (int, bool) {
	col := it.Column - 1
	if it.Line < 1 || it.Line > len(lines) || col < 2 || col > len(lines[it.Line-1].text) {
		return 0, false
	}
	before := lines[it.Line-1].text[:col]
	if !strings.HasSuffix(before, "- ") || !blank(before[:len(before)-2]) {
		return 0, false
	}
	return len(before) - 2, true
}

// itemExtent returns the lines [start, end) of a list item: the line of its
// "- " and the more deeply indented lines after it, without the blank lines
// that follow, and with the comment lines directly above it.
func itemExtent(lines []line, it *yaml.Node) (start, end int) {
	depth, _ := dash(lines, it)
	start = it.Line - 1
	for end = start + 1; end < len(lines); end++ {
		if text := lines[end].text; !blank(text) && indent(text) <= depth {
			break
		}
	}
	for end > start+1 && blank(lines[end-1].text) {
		end--
	}
	for start > 0 {
		above := lines[start-1].text
		if indent(above) != depth || !strings.HasPrefix(above[depth:], "#") {
			break
		}
		start--
	}
	return start, end
}

func insertItem(src []byte, path []any, at int, item string) ([]byte, error) {
	seq, lines, err := list(src, path)
	if err != nil {
		return nil, err
	}
	if at < 0 || at > len(seq.Content) {
		return nil, fmt.Errorf("has no position %d", at)
	}
	var value any
	if err := yaml.Unmarshal([]byte(item), &value); err != nil {
		return nil, fmt.Errorf("the new item is not valid: %w", err)
	}
	if _, ok := plain(value).(map[string]any); !ok {
		return nil, errors.New("the new item must be a mapping")
	}

	depth, _ := dash(lines, seq.Content[0])
	var entry []string
	for i, text := range strings.Split(strings.TrimRight(item, "\n"), "\n") {
		switch {
		case i == 0:
			text = strings.Repeat(" ", depth) + "- " + text
		case !blank(text):
			text = strings.Repeat(" ", depth+2) + text
		}
		entry = append(entry, text)
	}

	// Items set apart by blank lines get one between the new item and its
	// neighbour too. A list of one item is taken to be such a list if a
	// blank line follows it.
	apart := false
	for i := range seq.Content {
		if i+1 < len(seq.Content) || len(seq.Content) == 1 {
			_, end := itemExtent(lines, seq.Content[i])
			apart = apart || end < len(lines) && blank(lines[end].text)
		}
	}
	var pos int
	if at == len(seq.Content) {
		_, pos = itemExtent(lines, seq.Content[at-1])
		if apart {
			entry = append([]string{""}, entry...)
		}
	} else {
		pos, _ = itemExtent(lines, seq.Content[at])
		if apart {
			entry = append(entry, "")
		}
	}
	out := join(splice(lines, pos, pos, entry))

	return out, check(src, out, func(root any) (any, bool) {
		return changeList(root, path, func(items []any) []any {
			return slices.Insert(slices.Clone(items), at, plain(value))
		})
	}, true)
}

func removeItem(src []byte, path []any, at int) ([]byte, error) {
	seq, lines, err := list(src, path)
	if err != nil {
		return nil, err
	}
	switch {
	case at < 0 || at >= len(seq.Content):
		return nil, errors.New("there is no such item")
	case len(seq.Content) == 1:
		return nil, errors.New("is the only item of its list, so it cannot be removed")
	}
	start, end := itemExtent(lines, seq.Content[at])
	// The blank line that set the item apart goes with it: the one after it,
	// so that the next item takes its place, or for the last item the one
	// before, so that what follows the list keeps its distance.
	if at == len(seq.Content)-1 {
		if start > 0 && blank(lines[start-1].text) {
			start--
		}
	} else if end < len(lines) && blank(lines[end].text) {
		end++
	}
	out := join(splice(lines, start, end, nil))

	return out, check(src, out, func(root any) (any, bool) {
		return changeList(root, path, func(items []any) []any {
			return slices.Delete(slices.Clone(items), at, at+1)
		})
	}, false)
}

// changeList makes a change to the list at path of a decoded document. It
// reports false if the path does not lead to a list.
func changeList(root any, path []any, change func([]any) []any) (any, bool) {
	if len(path) == 0 {
		items, ok := root.([]any)
		if !ok {
			return nil, false
		}
		return change(items), true
	}
	switch p := path[0].(type) {
	case string:
		m, ok := root.(map[string]any)
		if !ok {
			return nil, false
		}
		child, ok := changeList(m[p], path[1:], change)
		if ok {
			m[p] = child
		}
		return m, ok
	case int:
		l, ok := root.([]any)
		if !ok || p < 0 || p >= len(l) {
			return nil, false
		}
		child, ok := changeList(l[p], path[1:], change)
		if ok {
			l[p] = child
		}
		return l, ok
	}
	return nil, false
}

// check reads an edited document back and compares it with what the change
// should have made of the original. With keepComments it also checks that no
// comment was lost.
func check(before, after []byte, change func(root any) (any, bool), keepComments bool) error {
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
	want, ok := change(plain(want))
	a, errA := json.Marshal(want)
	b, errB := json.Marshal(plain(got))
	if !ok || errA != nil || errB != nil || !bytes.Equal(a, b) {
		return errInPlace
	}
	if keepComments {
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

// Pair is one "key: value" of a mapping to be written. The value is a string,
// bool, number or nil, a []Pair holding a mapping below it, or Items holding
// a list below it.
type Pair struct {
	Key   string
	Value any
}

// Items is a list to be written as the value of a Pair. Each item is a
// string, bool, number or nil, or a []Pair holding a mapping.
type Items []any

// Mapping writes pairs as the text of a mapping, from the first column and
// in the order given, as InsertItem takes it.
func Mapping(pairs []Pair) (string, error) {
	lines, err := mapping(pairs, 0)
	if err != nil {
		return "", err
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func mapping(pairs []Pair, depth int) ([]string, error) {
	var lines []string
	pad := strings.Repeat(" ", depth)
	for _, p := range pairs {
		if items, ok := p.Value.(Items); ok {
			if len(items) == 0 {
				lines = append(lines, pad+scalar(p.Key, 0)+": []")
				continue
			}
			lines = append(lines, pad+scalar(p.Key, 0)+":")
			for _, item := range items {
				var inner []string
				var err error
				switch item := item.(type) {
				case []Pair:
					inner, err = mapping(item, 0)
				case string:
					inner = []string{scalar(item, 0)}
				case nil, bool, int, int64, float64:
					var out []byte
					out, err = yaml.Marshal(item)
					inner = []string{strings.TrimSpace(string(out))}
				default:
					err = fmt.Errorf("an item of type %T cannot be written", item)
				}
				if err != nil {
					return nil, fmt.Errorf("%s: %w", p.Key, err)
				}
				for i, l := range inner {
					switch {
					case i == 0:
						lines = append(lines, pad+"  - "+l)
					case l == "":
						lines = append(lines, "")
					default:
						lines = append(lines, pad+"    "+l)
					}
				}
			}
			continue
		}
		if below, ok := p.Value.([]Pair); ok {
			inner, err := mapping(below, depth+2)
			if err != nil {
				return nil, err
			}
			if len(inner) == 0 {
				lines = append(lines, pad+scalar(p.Key, 0)+": {}")
				continue
			}
			lines = append(append(lines, pad+scalar(p.Key, 0)+":"), inner...)
			continue
		}
		entry, err := render(pad, p.Key, 0, p.Value, 0, depth)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.Key, err)
		}
		lines = append(lines, entry...)
	}
	return lines, nil
}
