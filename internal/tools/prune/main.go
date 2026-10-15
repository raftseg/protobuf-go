// Copyright 2026 The raftseg authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command prune makes this fork from a checkout of upstream
// google.golang.org/protobuf. Every file the fork takes from upstream is
// written by this command, so following an upstream release is a matter of
// running it again (see FORK.md, Updating):
//
//	go run ./internal/tools/prune -upstream /path/to/protobuf-go
//
// It keeps the packages that generated code and etcd raft need (roots
// below) and every package they import, with their tests, under each set of
// build flags that selects other files. It also keeps every package those
// tests import, without their own tests, and copies github.com/google/go-cmp,
// which they use, into internal/gocmp, also without its tests. It
// rewrites import paths to this module, applies the patches below, formats
// the result, and records the files it wrote in upstream-files.txt. Files
// listed there by the previous run are removed first; files of the fork's
// own (README.md, FORK.md, internal/depcheck, this command, …) are never
// touched. Finally it updates the release and commit in upstream.json.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const (
	upstreamModule = "google.golang.org/protobuf"
	modulePath     = "github.com/raftseg/protobuf-go"
	cmpModule      = "github.com/google/go-cmp"
	cmpDir         = "internal/gocmp" // where go-cmp's tree is copied
	manifest       = "upstream-files.txt"
)

// roots are the packages the fork exists for: those that protoc-gen-go's
// generated code imports and those etcd raft imports (the runtime), and
// those a protoc plugin needs to read descriptors and answer protoc
// (raftseg/protobuf-go-lite's code generator).
var roots = []string{
	"proto",
	"encoding/protowire",
	"reflect/protoreflect",
	"reflect/protoregistry",
	"runtime/protoiface",
	"runtime/protoimpl",

	"encoding/prototext",
	"reflect/protodesc",
	"types/descriptorpb",
	"types/dynamicpb",
	"types/gofeaturespb",
	"types/pluginpb",
}

// buildVariants are the build flags whose files the closure must cover.
// Each selects files with other imports (internal/flags, internal/impl).
var buildVariants = [][]string{
	nil,
	{"-tags=protolegacy"},
	{"-tags=protoreflect"},
	{"-race"},
}

// A patch replaces one exact piece of an upstream file. FORK.md explains
// each.
type patch struct {
	file, old, new, note string
}

var patches = []patch{}

var importNote = "import paths rewritten for this\n// fork's module."

func main() {
	log.SetFlags(0)
	log.SetPrefix("prune: ")
	upstream := flag.String("upstream", "", "clean checkout of upstream protobuf-go at a release tag")
	dst := flag.String("dst", ".", "root of this fork")
	flag.Parse()
	if *upstream == "" || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*upstream, *dst); err != nil {
		log.Fatal(err)
	}
}

func run(up, dst string) error {
	up, err := filepath.Abs(up)
	if err != nil {
		return err
	}
	if b, err := os.ReadFile(filepath.Join(dst, "go.mod")); err != nil || !bytes.HasPrefix(b, []byte("module "+modulePath+"\n")) {
		return fmt.Errorf("%s is not the root of %s", dst, modulePath)
	}
	mod, err := readGoMod(filepath.Join(up, "go.mod"))
	if err != nil {
		return err
	}
	if mod.module != upstreamModule {
		return fmt.Errorf("%s is %s, not %s", up, mod.module, upstreamModule)
	}
	if out, err := command(up, "git", "status", "--porcelain"); err != nil || out != "" {
		return fmt.Errorf("upstream checkout is not clean: %v%s", err, out)
	}
	tag, err := command(up, "git", "describe", "--tags", "--exact-match", "HEAD")
	if err != nil {
		return fmt.Errorf("upstream checkout is not at a release tag: %v", err)
	}
	commit, err := command(up, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}

	pkgs, shipped, external, err := closure(up)
	if err != nil {
		return err
	}
	for _, p := range external {
		if p != cmpModule+"/cmp" && !strings.HasPrefix(p, cmpModule+"/cmp/") {
			return fmt.Errorf("the closure imports %s: only %s may be copied in", p, cmpModule)
		}
	}
	cmpVersion := mod.require[cmpModule]
	if len(external) > 0 && cmpVersion == "" {
		return fmt.Errorf("go.mod does not require %s", cmpModule)
	}

	// Remove what the previous run wrote.
	if err := removeListed(dst); err != nil {
		return err
	}

	w := &writer{dst: dst}
	for _, f := range []string{"LICENSE", "PATENTS"} {
		if err := w.copyFile(filepath.Join(up, f), f); err != nil {
			return err
		}
	}
	if err := w.copyFile(filepath.Join(up, "README.md"), "UPSTREAM-README.md"); err != nil {
		return err
	}
	for _, p := range pkgs {
		if err := w.copyPackage(filepath.Join(up, p), p, shipped[p]); err != nil {
			return err
		}
	}
	if len(external) > 0 {
		dir, err := command(up, "go", "mod", "download", "-json", cmpModule+"@"+cmpVersion)
		if err != nil {
			return err
		}
		var dl struct{ Dir string }
		if err := json.Unmarshal([]byte(dir), &dl); err != nil {
			return err
		}
		if err := w.copyTree(filepath.Join(dl.Dir, "cmp"), path.Join(cmpDir, "cmp"), false); err != nil {
			return err
		}
		if err := w.copyFile(filepath.Join(dl.Dir, "LICENSE"), path.Join(cmpDir, "LICENSE")); err != nil {
			return err
		}
	}
	for _, p := range patches {
		if !slices.Contains(w.files, p.file) {
			return fmt.Errorf("patch: %s was not copied", p.file)
		}
		if err := w.patch(p); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dst, "go.mod"), []byte("module "+modulePath+"\n\ngo "+mod.goVersion+"\n"), 0o666); err != nil {
		return err
	}
	sort.Strings(w.files)
	if err := os.WriteFile(filepath.Join(dst, manifest), []byte(strings.Join(w.files, "\n")+"\n"), 0o666); err != nil {
		return err
	}
	if err := updateUpstreamJSON(up, dst, tag, commit, cmpVersion); err != nil {
		return err
	}
	log.Printf("%s %s (%.12s): %d packages, %d files, %s %s", upstreamModule, tag, commit, len(pkgs), len(w.files), cmpModule, cmpVersion)
	return nil
}

// closure returns the upstream packages (as paths relative to the module)
// that the roots and their tests need, those of them the roots need (whose
// tests are kept), and the imports from outside the module and the standard
// library.
func closure(up string) (pkgs []string, shipped map[string]bool, external []string, err error) {
	inModule := func(p string) bool { return p == upstreamModule || strings.HasPrefix(p, upstreamModule+"/") }
	full := func(rel []string) []string {
		var out []string
		for _, r := range rel {
			out = append(out, upstreamModule+"/"+r)
		}
		return out
	}
	shipped = map[string]bool{}
	all := map[string]bool{}
	ext := map[string]bool{}
	for _, flags := range buildVariants {
		list := func(format string, pkgs ...string) ([]string, error) {
			args := append([]string{"list", "-e"}, flags...)
			args = append(args, "-f", format)
			args = append(args, pkgs...)
			out, err := command(up, "go", args...)
			if err != nil {
				return nil, err
			}
			return strings.Fields(out), nil
		}
		deps, err := list("{{.ImportPath}}", append([]string{"-deps"}, full(roots)...)...)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, p := range deps {
			if inModule(p) {
				shipped[p] = true
			}
		}
		var tests []string
		for p := range shipped {
			imps, err := list(`{{join .TestImports " "}} {{join .XTestImports " "}}`, p)
			if err != nil {
				return nil, nil, nil, err
			}
			tests = append(tests, imps...)
		}
		deps, err = list("{{.ImportPath}}", append([]string{"-deps"}, slices.Concat(slices.Sorted(mapKeys(shipped)), tests)...)...)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, p := range deps {
			switch {
			case inModule(p):
				all[p] = true
			case strings.Contains(strings.SplitN(p, "/", 2)[0], "."):
				ext[p] = true
			}
		}
	}
	rel := func(p string) string { return strings.TrimPrefix(strings.TrimPrefix(p, upstreamModule), "/") }
	for p := range all {
		pkgs = append(pkgs, rel(p))
	}
	sort.Strings(pkgs)
	if pkgs[0] == "" {
		return nil, nil, nil, errors.New("the closure includes the module's root package")
	}
	relShipped := map[string]bool{}
	for p := range shipped {
		relShipped[rel(p)] = true
	}
	return pkgs, relShipped, slices.Sorted(mapKeys(ext)), nil
}

func mapKeys(m map[string]bool) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

type writer struct {
	dst   string
	files []string // written, relative to dst, slash-separated
}

// copyPackage copies a package directory's files, and, if tests are kept,
// its tests and testdata.
func (w *writer) copyPackage(src, rel string, tests bool) error {
	ents, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range ents {
		switch {
		case e.Type().IsRegular():
			if !tests && strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			if err := w.copyFile(filepath.Join(src, e.Name()), path.Join(rel, e.Name())); err != nil {
				return err
			}
		case e.IsDir() && e.Name() == "testdata" && tests:
			if err := w.copyTree(filepath.Join(src, e.Name()), path.Join(rel, e.Name()), true); err != nil {
				return err
			}
		}
	}
	return nil
}

// copyTree copies a directory tree, with or without tests and testdata.
func (w *writer) copyTree(src, rel string, tests bool) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !tests && (d.IsDir() && d.Name() == "testdata" || strings.HasSuffix(d.Name(), "_test.go")) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		r, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		return w.copyFile(p, path.Join(rel, filepath.ToSlash(r)))
	})
}

var rewrites = strings.NewReplacer(
	`"`+upstreamModule, `"`+modulePath,
	`"`+cmpModule+"/cmp", `"`+modulePath+"/"+cmpDir+"/cmp",
)

// copyFile copies src to rel, rewriting the import paths of Go files
// outside testdata.
func (w *writer) copyFile(src, rel string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if strings.HasSuffix(rel, ".go") && !slices.Contains(strings.Split(rel, "/"), "testdata") {
		if s := rewrites.Replace(string(b)); s != string(b) {
			b, err = markModified([]byte(s), importNote, rel)
			if err != nil {
				return err
			}
		}
	}
	return w.write(rel, b)
}

func (w *writer) write(rel string, b []byte) error {
	if slices.Contains(w.files, rel) {
		return fmt.Errorf("%s written twice", rel)
	}
	dst := filepath.Join(w.dst, filepath.FromSlash(rel))
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("%s exists and is not listed in %s: it belongs to the fork", rel, manifest)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o777); err != nil {
		return err
	}
	if err := os.WriteFile(dst, b, 0o666); err != nil {
		return err
	}
	w.files = append(w.files, rel)
	return nil
}

func (w *writer) patch(p patch) error {
	name := filepath.Join(w.dst, filepath.FromSlash(p.file))
	b, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	if n := bytes.Count(b, []byte(p.old)); n != 1 {
		return fmt.Errorf("patch: %s contains the text to replace %d times", p.file, n)
	}
	b = bytes.Replace(b, []byte(p.old), []byte(p.new), 1)
	if b, err = markModified(b, p.note, p.file); err != nil {
		return err
	}
	return os.WriteFile(name, b, 0o666)
}

var modifiedRE = regexp.MustCompile(`(?m)^// Modified by the raftseg authors: (.*(?:\n// .*)*)$`)

// markModified notes a change in the file's header, below its copyright
// notice, and formats the file.
func markModified(b []byte, note, rel string) ([]byte, error) {
	s := string(b)
	if m := modifiedRE.FindStringSubmatchIndex(s); m != nil {
		// A second change: append it to the note.
		s = s[:m[1]] + ";\n// " + note + s[m[1]:]
	} else {
		lines := strings.SplitAfter(s, "\n")
		n := 0
		for n < len(lines) && strings.HasPrefix(lines[n], "//") {
			n++
		}
		text := "// Modified by the raftseg authors: " + note + "\n"
		if n > 0 && n < len(lines) && strings.TrimSpace(lines[n]) == "" {
			// After the leading comment (the copyright notice).
			s = strings.Join(lines[:n], "") + "//\n" + text + strings.Join(lines[n:], "")
		} else {
			// The leading comment is the package's documentation, or there
			// is none: a block of its own at the top.
			s = text + "\n" + s
		}
	}
	out, err := format.Source([]byte(s))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", rel, err)
	}
	return out, nil
}

// removeListed removes the files the previous run wrote, and directories
// left empty.
func removeListed(dst string) error {
	f, err := os.Open(filepath.Join(dst, manifest))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	defer f.Close()
	dirs := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		rel := sc.Text()
		if rel == "" || path.IsAbs(rel) || strings.HasPrefix(path.Clean(rel), "..") {
			return fmt.Errorf("%s: bad entry %q", manifest, rel)
		}
		if err := os.Remove(filepath.Join(dst, filepath.FromSlash(rel))); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		for d := path.Dir(rel); d != "."; d = path.Dir(d) {
			dirs[d] = true
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	// Deepest first, so that parents are empty by the time they come up.
	ds := slices.Sorted(mapKeys(dirs))
	sort.Slice(ds, func(i, j int) bool { return strings.Count(ds[i], "/") > strings.Count(ds[j], "/") })
	for _, d := range ds {
		if ents, err := os.ReadDir(filepath.Join(dst, d)); err == nil && len(ents) == 0 {
			if err := os.Remove(filepath.Join(dst, d)); err != nil {
				return err
			}
		}
	}
	return nil
}

type goMod struct {
	module, goVersion string
	require           map[string]string
}

// readGoMod reads the lines of go.mod that matter here.
func readGoMod(name string) (*goMod, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	m := &goMod{require: map[string]string{}}
	inRequire := false
	for _, line := range strings.Split(string(b), "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		switch {
		case len(f) == 0:
		case inRequire && f[0] == ")":
			inRequire = false
		case inRequire && len(f) == 2:
			m.require[f[0]] = f[1]
		case f[0] == "module" && len(f) == 2:
			m.module = f[1]
		case f[0] == "go" && len(f) == 2:
			m.goVersion = f[1]
		case f[0] == "require" && len(f) == 2 && f[1] == "(":
			inRequire = true
		case f[0] == "require" && len(f) == 3:
			m.require[f[1]] = f[2]
		}
	}
	if m.module == "" || m.goVersion == "" {
		return nil, fmt.Errorf("%s: no module or go line", name)
	}
	return m, nil
}

// upstreamJSON is upstream.json, in the format of raft's (which raftseg's
// SBOM tool reads), with the modules copied in.
type upstreamJSON struct {
	Comment      string `json:"comment"`
	Module       string `json:"module"`
	Version      string `json:"version"`
	Repository   string `json:"repository"`
	Commit       string `json:"commit"`
	Sum          string `json:"sum"`
	License      string `json:"license"`
	Copyright    string `json:"copyright"`
	Changes      string `json:"changes"`
	CherryPicked []struct {
		Commit  string `json:"commit"`
		Subject string `json:"subject"`
	} `json:"cherryPicked"`
	CopiedIn []struct {
		Module     string `json:"module"`
		Version    string `json:"version"`
		Repository string `json:"repository"`
		License    string `json:"license"`
		Copyright  string `json:"copyright"`
		Path       string `json:"path"`
		Scope      string `json:"scope"`
	} `json:"copiedIn"`
}

// updateUpstreamJSON records the release the fork now follows. The other
// fields are kept as they are.
func updateUpstreamJSON(up, dst, tag, commit, cmpVersion string) error {
	name := filepath.Join(dst, "upstream.json")
	b, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	var u upstreamJSON
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&u); err != nil {
		return fmt.Errorf("upstream.json: %v", err)
	}
	if u.Module != upstreamModule {
		return fmt.Errorf("upstream.json: module %q", u.Module)
	}
	out, err := command(up, "go", "mod", "download", "-json", upstreamModule+"@"+tag)
	if err != nil {
		return err
	}
	var dl struct{ Sum string }
	if err := json.Unmarshal([]byte(out), &dl); err != nil {
		return err
	}
	if u.Version != tag {
		u.CherryPicked = u.CherryPicked[:0] // picks from an older release are in the new one
	}
	u.Version, u.Commit, u.Sum = tag, commit, dl.Sum
	for i := range u.CopiedIn {
		if u.CopiedIn[i].Module == cmpModule {
			u.CopiedIn[i].Version = cmpVersion
		}
	}
	if b, err = json.MarshalIndent(u, "", "  "); err != nil {
		return err
	}
	return os.WriteFile(name, append(b, '\n'), 0o666)
}

// command runs a command in dir with modules on and the workspace off, and
// returns its standard output without the final newline.
func command(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GO111MODULE=on")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, stderr.Bytes())
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}
