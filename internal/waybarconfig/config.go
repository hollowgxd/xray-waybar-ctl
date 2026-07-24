// Package waybarconfig adds the xray-waybar custom module to an existing
// Waybar JSON/JSONC config without reformatting it or dropping comments.
package waybarconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const moduleName = "custom/vpn"

type nodeKind uint8

const (
	kindUnknown nodeKind = iota
	kindObject
	kindArray
	kindString
	kindLiteral
)

type node struct {
	kind       nodeKind
	start, end int
	text       string
	properties []property
	items      []*node
}

type property struct {
	key      string
	keyStart int
	value    *node
	comma    int
}

type parser struct {
	src []byte
	pos int
}

type edit struct {
	pos  int
	text string
}

// Result describes a file integration.
type Result struct {
	ConfigPath string
	BackupPath string
	Changed    bool
}

// Locate finds the config used by a running Waybar process first, then
// falls back to Waybar's standard XDG paths. An explicit path always wins.
func Locate(explicit string) (string, error) {
	if explicit != "" {
		return resolveExisting(explicit)
	}
	if active := runningWaybarConfig(); active != "" {
		if resolved, err := resolveExisting(active); err == nil {
			return resolved, nil
		}
	}

	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("waybar config: home directory: %w", err)
		}
		configHome = filepath.Join(home, ".config")
	}
	for _, name := range []string{"config.jsonc", "config", "config.json"} {
		path := filepath.Join(configHome, "waybar", name)
		if resolved, err := resolveExisting(path); err == nil {
			return resolved, nil
		}
	}
	return "", errors.New("Waybar config not found (tried the running process and ~/.config/waybar/config.jsonc|config)")
}

func resolveExisting(path string) (string, error) {
	expanded, err := expandHome(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(expanded) {
		expanded, err = filepath.Abs(expanded)
		if err != nil {
			return "", err
		}
	}
	resolved, err := filepath.EvalSymlinks(expanded)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", resolved)
	}
	return resolved, nil
}

func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func runningWaybarConfig() string {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		procDir := filepath.Join("/proc", entry.Name())
		raw, err := os.ReadFile(filepath.Join(procDir, "cmdline"))
		if err != nil || len(raw) == 0 {
			continue
		}
		args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
		if len(args) == 0 || filepath.Base(args[0]) != "waybar" {
			continue
		}
		for i := 1; i < len(args); i++ {
			var candidate string
			switch {
			case args[i] == "-c" || args[i] == "--config":
				if i+1 < len(args) {
					candidate = args[i+1]
				}
			case strings.HasPrefix(args[i], "--config="):
				candidate = strings.TrimPrefix(args[i], "--config=")
			}
			if candidate == "" {
				continue
			}
			if filepath.IsAbs(candidate) || strings.HasPrefix(candidate, "~/") {
				return candidate
			}
			if cwd, err := os.Readlink(filepath.Join(procDir, "cwd")); err == nil {
				return filepath.Join(cwd, candidate)
			}
			return candidate
		}
	}
	return ""
}

// Install patches path atomically and writes a timestamped backup beside it.
// position accepts left, center or right and defaults to right.
func Install(path, binaryPath, position string) (Result, error) {
	resolved, err := Locate(path)
	if err != nil {
		return Result{}, err
	}
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return Result{}, fmt.Errorf("read Waybar config: %w", err)
	}
	patched, changed, err := Patch(raw, binaryPath, position)
	if err != nil {
		return Result{}, fmt.Errorf("patch %s: %w", resolved, err)
	}
	result := Result{ConfigPath: resolved, Changed: changed}
	if !changed {
		return result, nil
	}

	info, err := os.Stat(resolved)
	if err != nil {
		return Result{}, err
	}
	stamp := time.Now().Format("20060102-150405.000000000")
	backup := resolved + ".xray-waybar-backup-" + stamp
	if err := os.WriteFile(backup, raw, info.Mode().Perm()); err != nil {
		return Result{}, fmt.Errorf("write Waybar backup: %w", err)
	}
	if err := writeAtomic(resolved, patched, info.Mode().Perm()); err != nil {
		_ = os.Remove(backup)
		return Result{}, err
	}
	result.BackupPath = backup
	return result, nil
}

func writeAtomic(path string, raw []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".xray-waybar-config-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace Waybar config: %w", err)
	}
	return nil
}

// Patch returns a minimally edited JSONC document. Existing module settings
// and placement are never overwritten, which keeps the result fully
// customizable through normal Waybar configuration.
func Patch(src []byte, binaryPath, position string) ([]byte, bool, error) {
	position = strings.ToLower(strings.TrimSpace(position))
	if position == "" {
		position = "right"
	}
	if position != "left" && position != "center" && position != "right" {
		return nil, false, fmt.Errorf("position must be left, center or right")
	}
	if binaryPath == "" {
		return nil, false, errors.New("binary path is required")
	}

	p := &parser{src: src}
	root, err := p.parseValue()
	if err != nil {
		return nil, false, err
	}
	p.skipTrivia()
	if p.pos != len(src) {
		return nil, false, fmt.Errorf("unexpected content at byte %d", p.pos)
	}

	target := selectBarObject(root, position)
	if target == nil {
		return nil, false, errors.New("Waybar config must be a JSON object or an array of objects")
	}

	var edits []edit
	listed := moduleIsListed(target)
	positionProp := findProperty(target, "modules-"+position)
	needPositionProperty := false
	if !listed {
		if positionProp != nil {
			if positionProp.value.kind != kindArray {
				return nil, false, fmt.Errorf("%q must be an array", positionProp.key)
			}
			edits = append(edits, insertArrayItem(src, positionProp, moduleName))
		} else {
			needPositionProperty = true
		}
	}

	needDefinition := findProperty(target, moduleName) == nil
	if needPositionProperty || needDefinition {
		additions := make([]string, 0, 2)
		if needPositionProperty {
			additions = append(additions, strconv.Quote("modules-"+position)+": ["+strconv.Quote(moduleName)+"]")
		}
		if needDefinition {
			additions = append(additions, moduleDefinition(binaryPath))
		}
		objectEdits, err := appendObjectProperties(src, target, additions)
		if err != nil {
			return nil, false, err
		}
		edits = append(edits, objectEdits...)
	}

	if len(edits) == 0 {
		return append([]byte(nil), src...), false, nil
	}
	for i := 1; i < len(edits); i++ {
		for j := i; j > 0 && edits[j-1].pos < edits[j].pos; j-- {
			edits[j-1], edits[j] = edits[j], edits[j-1]
		}
	}
	out := append([]byte(nil), src...)
	for _, e := range edits {
		if e.pos < 0 || e.pos > len(out) {
			return nil, false, errors.New("internal Waybar edit offset is invalid")
		}
		out = append(out[:e.pos], append([]byte(e.text), out[e.pos:]...)...)
	}
	return out, true, nil
}

func selectBarObject(root *node, position string) *node {
	if root.kind == kindObject {
		return root
	}
	if root.kind != kindArray {
		return nil
	}
	var first *node
	for _, item := range root.items {
		if item.kind != kindObject {
			continue
		}
		if first == nil {
			first = item
		}
		if findProperty(item, "modules-"+position) != nil {
			return item
		}
	}
	return first
}

func findProperty(obj *node, key string) *property {
	for i := range obj.properties {
		if obj.properties[i].key == key {
			return &obj.properties[i]
		}
	}
	return nil
}

func moduleIsListed(obj *node) bool {
	for _, position := range []string{"left", "center", "right"} {
		prop := findProperty(obj, "modules-"+position)
		if prop == nil || prop.value.kind != kindArray {
			continue
		}
		for _, item := range prop.value.items {
			if item.kind == kindString && item.text == moduleName {
				return true
			}
		}
	}
	return false
}

func insertArrayItem(src []byte, prop *property, value string) edit {
	arr := prop.value
	inner := src[arr.start+1 : arr.end-1]
	multiline := bytes.Contains(inner, []byte{'\n'})
	quoted := strconv.Quote(value)
	if !multiline {
		if len(arr.items) == 0 {
			return edit{pos: arr.start + 1, text: quoted}
		}
		return edit{pos: arr.start + 1, text: quoted + ", "}
	}

	indent := ""
	if len(arr.items) > 0 {
		indent = lineIndent(src, arr.items[0].start)
	}
	if indent == "" {
		indent = lineIndent(src, prop.keyStart) + detectIndentUnit(src, prop.keyStart)
	}
	suffix := ""
	if len(arr.items) > 0 {
		suffix = ","
	}
	return edit{pos: arr.start + 1, text: "\n" + indent + quoted + suffix}
}

func appendObjectProperties(src []byte, obj *node, additions []string) ([]edit, error) {
	if len(additions) == 0 {
		return nil, nil
	}
	closePos := obj.end - 1
	multiline := bytes.Contains(src[obj.start:obj.end], []byte{'\n'})
	if !multiline {
		prefix := ""
		if len(obj.properties) > 0 {
			if obj.properties[len(obj.properties)-1].comma >= 0 {
				prefix = " "
			} else {
				prefix = ", "
			}
		}
		return []edit{{pos: closePos, text: prefix + strings.Join(additions, ", ")}}, nil
	}

	objectIndent := lineIndent(src, obj.start)
	propIndent := objectIndent + detectIndentUnit(src, obj.start)
	if len(obj.properties) > 0 {
		if detected := lineIndent(src, obj.properties[0].keyStart); detected != "" {
			propIndent = detected
		}
	}
	rendered := make([]string, 0, len(additions))
	for _, addition := range additions {
		rendered = append(rendered, indentMultiline(addition, propIndent))
	}

	insertPos := closePos
	if start := lineStart(src, closePos); onlyWhitespace(src[start:closePos]) {
		insertPos = start
	}
	var edits []edit
	if len(obj.properties) > 0 {
		last := obj.properties[len(obj.properties)-1]
		if last.comma < 0 {
			edits = append(edits, edit{pos: last.value.end, text: ","})
		}
	}
	edits = append(edits, edit{pos: insertPos, text: strings.Join(rendered, ",\n") + "\n"})
	return edits, nil
}

func moduleDefinition(binaryPath string) string {
	command := func(arg string) string {
		return strconv.Quote(binaryPath + " " + arg)
	}
	return strings.Join([]string{
		strconv.Quote(moduleName) + ": {",
		"  \"exec\": " + command("status") + ",",
		"  \"return-type\": \"json\",",
		"  \"interval\": 5,",
		"  \"format\": \"{}\",",
		"  \"on-click\": " + command("toggle") + ",",
		"  \"on-click-right\": " + command("reconnect") + ",",
		"  \"on-scroll-up\": " + command("use-next") + ",",
		"  \"on-scroll-down\": " + command("use-prev"),
		"}",
	}, "\n")
}

func indentMultiline(value, baseIndent string) string {
	lines := strings.Split(value, "\n")
	for i := range lines {
		lines[i] = baseIndent + lines[i]
	}
	return strings.Join(lines, "\n")
}

func detectIndentUnit(src []byte, pos int) string {
	base := lineIndent(src, pos)
	lineEnd := bytes.IndexByte(src[pos:], '\n')
	searchEnd := len(src)
	if lineEnd >= 0 {
		searchEnd = pos + lineEnd + 1
	}
	for searchEnd < len(src) {
		nextEnd := bytes.IndexByte(src[searchEnd:], '\n')
		if nextEnd < 0 {
			nextEnd = len(src) - searchEnd
		}
		line := src[searchEnd : searchEnd+nextEnd]
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) > 0 {
			indent := string(line[:len(line)-len(trimmed)])
			if strings.HasPrefix(indent, base) && len(indent) > len(base) {
				return indent[len(base):]
			}
		}
		searchEnd += nextEnd + 1
	}
	return "  "
}

func lineStart(src []byte, pos int) int {
	if pos > len(src) {
		pos = len(src)
	}
	if idx := bytes.LastIndexByte(src[:pos], '\n'); idx >= 0 {
		return idx + 1
	}
	return 0
}

func lineIndent(src []byte, pos int) string {
	start := lineStart(src, pos)
	end := start
	for end < len(src) && (src[end] == ' ' || src[end] == '\t') {
		end++
	}
	return string(src[start:end])
}

func onlyWhitespace(raw []byte) bool {
	return len(bytes.TrimSpace(raw)) == 0
}

func (p *parser) parseValue() (*node, error) {
	p.skipTrivia()
	if p.pos >= len(p.src) {
		return nil, errors.New("unexpected end of JSONC")
	}
	switch p.src[p.pos] {
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case '"':
		return p.parseString()
	default:
		return p.parseLiteral()
	}
}

func (p *parser) parseObject() (*node, error) {
	obj := &node{kind: kindObject, start: p.pos}
	p.pos++
	for {
		p.skipTrivia()
		if p.pos >= len(p.src) {
			return nil, errors.New("unterminated object")
		}
		if p.src[p.pos] == '}' {
			p.pos++
			obj.end = p.pos
			return obj, nil
		}
		key, err := p.parseString()
		if err != nil {
			return nil, fmt.Errorf("object key at byte %d: %w", p.pos, err)
		}
		p.skipTrivia()
		if p.pos >= len(p.src) || p.src[p.pos] != ':' {
			return nil, fmt.Errorf("expected colon after %q at byte %d", key.text, p.pos)
		}
		p.pos++
		value, err := p.parseValue()
		if err != nil {
			return nil, fmt.Errorf("property %q: %w", key.text, err)
		}
		prop := property{key: key.text, keyStart: key.start, value: value, comma: -1}
		p.skipTrivia()
		if p.pos < len(p.src) && p.src[p.pos] == ',' {
			prop.comma = p.pos
			p.pos++
		} else if p.pos >= len(p.src) || p.src[p.pos] != '}' {
			return nil, fmt.Errorf("expected comma or object end at byte %d", p.pos)
		}
		obj.properties = append(obj.properties, prop)
	}
}

func (p *parser) parseArray() (*node, error) {
	arr := &node{kind: kindArray, start: p.pos}
	p.pos++
	for {
		p.skipTrivia()
		if p.pos >= len(p.src) {
			return nil, errors.New("unterminated array")
		}
		if p.src[p.pos] == ']' {
			p.pos++
			arr.end = p.pos
			return arr, nil
		}
		item, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		arr.items = append(arr.items, item)
		p.skipTrivia()
		if p.pos < len(p.src) && p.src[p.pos] == ',' {
			p.pos++
		} else if p.pos >= len(p.src) || p.src[p.pos] != ']' {
			return nil, fmt.Errorf("expected comma or array end at byte %d", p.pos)
		}
	}
}

func (p *parser) parseString() (*node, error) {
	start := p.pos
	if p.src[p.pos] != '"' {
		return nil, fmt.Errorf("expected string")
	}
	p.pos++
	escaped := false
	for p.pos < len(p.src) {
		c := p.src[p.pos]
		p.pos++
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			raw := string(p.src[start:p.pos])
			text, err := strconv.Unquote(raw)
			if err != nil {
				return nil, err
			}
			return &node{kind: kindString, start: start, end: p.pos, text: text}, nil
		}
	}
	return nil, errors.New("unterminated string")
}

func (p *parser) parseLiteral() (*node, error) {
	start := p.pos
	for p.pos < len(p.src) {
		if p.pos+1 < len(p.src) && p.src[p.pos] == '/' &&
			(p.src[p.pos+1] == '/' || p.src[p.pos+1] == '*') {
			if p.pos == start {
				return nil, fmt.Errorf("unexpected comment at %d", p.pos)
			}
			return &node{kind: kindLiteral, start: start, end: p.pos}, nil
		}
		switch p.src[p.pos] {
		case ',', ']', '}', ' ', '\t', '\r', '\n':
			if p.pos == start {
				return nil, fmt.Errorf("unexpected byte %q at %d", p.src[p.pos], p.pos)
			}
			return &node{kind: kindLiteral, start: start, end: p.pos}, nil
		default:
			p.pos++
		}
	}
	if p.pos == start {
		return nil, fmt.Errorf("expected value at byte %d", start)
	}
	return &node{kind: kindLiteral, start: start, end: p.pos}, nil
}

func (p *parser) skipTrivia() {
	for p.pos < len(p.src) {
		switch {
		case p.src[p.pos] == ' ' || p.src[p.pos] == '\t' || p.src[p.pos] == '\r' || p.src[p.pos] == '\n':
			p.pos++
		case p.pos+1 < len(p.src) && p.src[p.pos] == '/' && p.src[p.pos+1] == '/':
			p.pos += 2
			for p.pos < len(p.src) && p.src[p.pos] != '\n' {
				p.pos++
			}
		case p.pos+1 < len(p.src) && p.src[p.pos] == '/' && p.src[p.pos+1] == '*':
			p.pos += 2
			for p.pos+1 < len(p.src) && !(p.src[p.pos] == '*' && p.src[p.pos+1] == '/') {
				p.pos++
			}
			if p.pos+1 < len(p.src) {
				p.pos += 2
			}
		default:
			return
		}
	}
}
