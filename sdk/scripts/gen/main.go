// Command gen reads sdk/spec/openapi.json, checks it, and writes each SDK's generated file (types, enums, the
// operation and event tables, the version) plus the version in each SDK's manifest.
//
//	cd sdk/scripts/gen && go run .
//
// CI runs it and fails on a diff, so a spec change without regenerated SDKs never lands.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

// lang is one SDK's output. Each lang_*.go file registers its own.
type lang struct {
	name     string
	out      string                       // generated file, relative to sdk/
	tmpl     string                       // template file in templates/
	funcs    template.FuncMap             // language helpers for the template
	format   func([]byte) ([]byte, error) // optional formatter
	manifest []manifestVersion            // where the version is written outside the generated file
}

// manifestVersion rewrites the first match of re in file: group 1, the version, group 2.
type manifestVersion struct {
	file string
	re   *regexp.Regexp
}

var langs []lang

func main() {
	root := flag.String("root", "../..", "the sdk/ directory")
	only := flag.String("lang", "", "generate one language only")
	flag.Parse()
	if err := run(*root, *only); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(root, only string) error {
	raw, err := os.ReadFile(filepath.Join(root, "spec", "openapi.json"))
	if err != nil {
		return err
	}
	s, err := load(raw)
	if err != nil {
		return err
	}
	for _, l := range langs {
		if only != "" && l.name != only {
			continue
		}
		if err := emit(root, l, s); err != nil {
			return fmt.Errorf("%s: %w", l.name, err)
		}
	}
	return nil
}

func emit(root string, l lang, s *Spec) error {
	funcs := template.FuncMap{
		"pascal": pascal, "camel": camel, "snake": snake, "upper": strings.ToUpper, "join": strings.Join,
		"quote": func(v string) string { return fmt.Sprintf("%q", v) },
		"lines": func(prefix, text string) string { return wrap(prefix, text) },
	}
	for k, v := range l.funcs {
		funcs[k] = v
	}
	t, err := template.New(l.tmpl).Funcs(funcs).ParseFiles(filepath.Join("templates", l.tmpl))
	if err != nil {
		return err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, s); err != nil {
		return err
	}
	out := b.Bytes()
	if l.format != nil {
		if out, err = l.format(out); err != nil {
			return fmt.Errorf("format: %w\n%s", err, b.Bytes())
		}
	}
	if err := os.WriteFile(filepath.Join(root, l.out), out, 0o644); err != nil {
		return err
	}
	for _, m := range l.manifest {
		p := filepath.Join(root, m.file)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		loc := m.re.FindSubmatchIndex(b)
		if loc == nil {
			return fmt.Errorf("%s: no version matching %s", m.file, m.re)
		}
		b = append(append(append([]byte{}, b[:loc[2]]...), s.Version...), b[loc[3]:]...)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Words splits a snake_case or kebab-case name: "time-sensitive" -> [time sensitive].
func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == '.' || r == ' ' })
}

func pascal(s string) string {
	var b strings.Builder
	for _, w := range words(s) {
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}

func camel(s string) string {
	p := pascal(s)
	if p == "" {
		return p
	}
	return strings.ToLower(p[:1]) + p[1:]
}

func snake(s string) string { return strings.Join(words(s), "_") }

// wrap turns a description into comment lines, each starting with prefix, at most about 116 columns wide.
func wrap(prefix, text string) string {
	var out []string
	line := ""
	for _, w := range strings.Fields(text) {
		if line != "" && len(prefix)+len(line)+1+len(w) > 116 {
			out = append(out, prefix+line)
			line = w
			continue
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		out = append(out, prefix+line)
	}
	return strings.Join(out, "\n")
}
