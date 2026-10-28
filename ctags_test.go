package ctags

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/google/go-cmp/cmp"
	"github.com/hexops/autogold"
)

func TestParser(t *testing.T) {
	var info, debug *log.Logger
	if testing.Verbose() {
		info = log.New(os.Stderr, "INF: ", log.LstdFlags)
		debug = log.New(os.Stderr, "DBG: ", log.LstdFlags)
	}

	p, err := New(Options{
		Bin:   os.Getenv("CTAGS_COMMAND"),
		Info:  info,
		Debug: debug,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	type tc struct {
		path string
		data string
	}

	cases := []tc{
		{
			path: "com/sourcegraph/A.java",
			data: `
package com.sourcegraph;
import a.b.c;
class A implements B extends C {
  public static int D = 1;
  public int E;
  public A() {
    E = 2;
  }
  public int F() {
    E++;
  }
}
`,
		},
		{
			path: "schema.graphql",
			data: `
schema {
    query: Query
    mutation: Mutation
}
"""
An object with an ID.
"""
interface Node {
    """
    The ID of the node.
    """
    id: ID!
}
`,
		},
		{
			path: "test.groovy",
			data: `
package abc

int a = 1
String b = ''
List<String> c = [1, 2, 3]
// For simplicity, only support 1-level deep generics, so below won't be picked up
List<List<String>> d = []
Map<String, String> e = [a: 1, b: 2]
def f() {
  return 1
}
def g = {x -> x}
`,
		},
	}

	// Add cases which break ctags. Ensure we handle it gracefully
	paths, err := filepath.Glob("testdata/bad/*")
	if err != nil || len(paths) == 0 {
		t.Fatal(err)
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(p)
		}
		cases = append(cases, tc{
			path: filepath.Base(p),
			data: string(b),
		})
	}

	for _, tc := range cases {
		got, err := p.Parse(tc.path, []byte(tc.data))
		if err != nil {
			t.Error(err)
		}

		t.Run(tc.path, func(t *testing.T) {
			autogold.Equal(t, got, autogold.Name(strings.ReplaceAll(tc.path, "/", "_")))
		})
	}
}

func TestScanner(t *testing.T) {
	size := 20
	maxLen := 2*size + 1

	input := strings.Join([]string{
		"aaaaaaaaa",
		strings.Repeat("B", 3*size+3),
		strings.Repeat("C", size) + strings.Repeat("D", size+1),
		"",
		strings.Repeat("e", size-1),
		"f\r",
		strings.Repeat("G", maxLen+1),
		"gg",
	}, "\n")
	// Lines longer than the buffer are kept. Lines longer than maxLen are
	// skipped: the B's, and the G's which are one byte over.
	want := []string{
		"aaaaaaaaa",
		strings.Repeat("C", size) + strings.Repeat("D", size+1),
		strings.Repeat("e", size-1),
		"f",
		"gg",
	}

	var got []string
	r := newScanner(strings.NewReader(input), size, maxLen)
	for r.Scan() {
		got = append(got, string(r.Bytes()))
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}

	if !cmp.Equal(got, want) {
		t.Errorf("mismatch (-want +got):\n%s", cmp.Diff(want, got))
	}
}

// TestScannerBufferBoundary pins the 4,096-byte boundary of issue #27: with
// the 4,096-byte buffer that New uses, a 4,095-byte line fits and a 4,096-byte
// line does not. Both must be returned, and only the second is reported as
// long: the lines that were skipped before the fix.
func TestScannerBufferBoundary(t *testing.T) {
	const size = 4096
	lines := []string{
		strings.Repeat("a", size-1),
		strings.Repeat("b", size),
		strings.Repeat("c", 3*size),
		"d",
	}
	wantLong := []bool{false, true, true, false}

	r := newScanner(strings.NewReader(strings.Join(lines, "\n")+"\n"), size, 1<<20)
	var got []string
	var gotLong []bool
	for r.Scan() {
		got = append(got, string(r.Bytes()))
		gotLong = append(gotLong, r.long)
	}
	if err := r.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(lines) {
		t.Fatalf("got %d lines, want %d", len(got), len(lines))
	}
	for i := range lines {
		if got[i] != lines[i] {
			t.Errorf("line %d: got %d bytes, want %d", i, len(got[i]), len(lines[i]))
		}
	}
	if !cmp.Equal(gotLong, wantLong) {
		t.Errorf("long: got %v, want %v", gotLong, wantLong)
	}
}

// FuzzScanner checks what callers rely on: lines come back in order and
// intact (without "\r\n"), every non-empty line shorter than maxLen comes
// back, and no line longer than maxLen does.
func FuzzScanner(f *testing.F) {
	f.Add([]byte("a\nbb\r\n\n\r\nccc"), uint8(0), uint16(2))
	f.Add([]byte(strings.Repeat("x", 40)+"\n"+strings.Repeat("y", 15)+"\r\nz"), uint8(0), uint16(39))
	f.Add([]byte(strings.Repeat("x", 16)), uint8(0), uint16(100))
	f.Add([]byte(strings.Repeat("x", 20)+"\nok"), uint8(0), uint16(16)) // first chunk is exactly maxLen
	f.Fuzz(func(t *testing.T, data []byte, sz uint8, ml uint16) {
		size := 16 + int(sz%48) // bufio's minimum is 16
		maxLen := int(ml % 256)

		var got []string
		r := newScanner(bytes.NewReader(data), size, maxLen)
		for r.Scan() {
			got = append(got, string(r.Bytes()))
		}
		if err := r.Err(); err != nil {
			t.Fatal(err)
		}
		if r.Scan() {
			t.Fatal("Scan returned true after returning false")
		}

		// Walk the input lines and the returned lines together.
		rest := got
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(rest) > 0 && rest[0] == string(line) {
				if len(line) > maxLen {
					t.Fatalf("size=%d maxLen=%d: returned a %d-byte line", size, maxLen, len(line))
				}
				rest = rest[1:]
				continue
			}
			if len(line) > 0 && len(line) < maxLen {
				t.Fatalf("size=%d maxLen=%d: dropped %q", size, maxLen, line)
			}
		}
		if len(rest) > 0 {
			t.Fatalf("size=%d maxLen=%d: returned %q, not an input line in order", size, maxLen, rest[0])
		}
	})
}

func TestLanguageMapping(t *testing.T) {
	mapping, err := ListLanguageMappings(context.Background(), os.Getenv("CTAGS_COMMAND"))
	if err != nil {
		t.Fatal(err)
	}

	list, ok := mapping["JavaScript"]
	if !ok {
		t.Fatalf("expected 'JavaScript' in mapping. Mapping: %v", mapping)
	}

	expectedList := []string{"*.js", "*.jsx", "*.mjs"}
	if diff := cmp.Diff(list, expectedList); diff != "" {
		t.Fatalf("unexpected mappings list for 'JavaScript': got=%v expected=%v", list, expectedList)
	}
}

// newScanner returns a scanner over r with a read buffer of size bytes.
func newScanner(r io.Reader, size, maxLen int) *scanner {
	return &scanner{r: bufio.NewReaderSize(r, size), maxLen: maxLen}
}

// setOutputBudget lowers the budget of p: max(minBudget, ratio*len(content)).
func setOutputBudget(p Parser, ratio, minBudget int) {
	p.(*ctagsProcess).ratio, p.(*ctagsProcess).minBudget = ratio, minBudget
}

// Tests for https://github.com/sourcegraph/go-ctags/issues/27: tags whose
// reply line has 4,096 bytes or more. They assert names, lines and parents
// (not goldens) so that they hold on ctags 6.0.0 and 6.2.1 alike.

func newTestParser(t *testing.T, info *bytes.Buffer) Parser {
	t.Helper()
	opts := Options{Bin: os.Getenv("CTAGS_COMMAND")}
	if info != nil {
		opts.Info = log.New(info, "", 0)
	}
	p, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if info != nil {
		// ctags 6.2 logs initialization warnings (TOML, Cargo) that 6.0
		// does not. Only check what Parse logs.
		info.Reset()
	}
	return p
}

// tag is the part of an Entry the tests check. Long strings are shortened
// to a prefix and a length so that failures stay readable.
type tag struct {
	Name       string
	Line       int
	Parent     string
	ParentKind string
}

func short(s string) string {
	if len(s) <= 16 {
		return s
	}
	return fmt.Sprintf("%s...(%d)", s[:8], len(s))
}

func tags(es []*Entry) []tag {
	var ts []tag
	for _, e := range es {
		ts = append(ts, tag{short(e.Name), e.Line, short(e.Parent), e.ParentKind})
	}
	return ts
}

func TestParseLongLines(t *testing.T) {
	p := newTestParser(t, nil)

	bigClass := "Big" + strings.Repeat("x", 5000)
	bigStruct := "T" + strings.Repeat("y", 5000)
	issueFunc := "Long" + strings.Repeat("x", 5000) // the issue's example: 5,004 B
	func32K := "F" + strings.Repeat("x", 32<<10)    // inside the 4–64 KiB range that worked before Zoekt 5851b63
	func100K := "F" + strings.Repeat("y", 100<<10)  // beyond 64 KiB, under the cap
	var params []string
	for i := 0; i < 500; i++ {
		params = append(params, fmt.Sprintf("int p%d", i))
	}

	cases := []struct {
		name, path, src string
		want            []tag
	}{{
		// S1: children repeat the parent's full name in scope.
		name: "python class subtree",
		path: "a.py",
		src:  "class " + bigClass + ":\n    def a(self): pass\n    def b(self): pass\n\ndef tail(): pass\n",
		want: []tag{
			{short(bigClass), 1, "", ""},
			{"a", 2, short(bigClass), "class"},
			{"b", 3, short(bigClass), "class"},
			{"tail", 5, "", ""},
		},
	}, {
		name: "go struct subtree",
		path: "t.go",
		src:  "package p\n\ntype " + bigStruct + " struct {\n\tA int\n\tB string\n}\n",
		want: []tag{
			{"p", 1, "", ""},
			{short(bigStruct), 3, "p", "package"},
			{"A", 4, short("p." + bigStruct), "struct"},
			{"B", 5, short("p." + bigStruct), "struct"},
		},
	}, {
		// S2: ctags does not cut the name.
		name: "go func name from issue",
		path: "sample.go",
		src:  "package sample\n\nfunc Short() {}\n\nfunc " + issueFunc + "() {}\n\nfunc After() {}\n",
		want: []tag{
			{"sample", 1, "", ""},
			{"Short", 3, "sample", "package"},
			{short(issueFunc), 5, "sample", "package"},
			{"After", 7, "sample", "package"},
		},
	}, {
		// S2: ctags does not cut the signature (checked below).
		name: "c signature from issue",
		path: "c.c",
		src:  "int f(" + strings.Join(params, ", ") + ") { return 0; }\n",
		want: []tag{{"f", 1, "", ""}},
	}, {
		// S3
		name: "32 KiB name",
		path: "m.go",
		src:  "package m\n\nfunc " + func32K + "() {}\n",
		want: []tag{{"m", 1, "", ""}, {short(func32K), 3, "m", "package"}},
	}, {
		name: "100 KiB name",
		path: "h.go",
		src:  "package h\n\nfunc " + func100K + "() {}\n",
		want: []tag{{"h", 1, "", ""}, {short(func100K), 3, "h", "package"}},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			es, err := p.Parse(tc.path, []byte(tc.src))
			if err != nil {
				t.Fatal(err)
			}
			if d := cmp.Diff(tc.want, tags(es)); d != "" {
				t.Fatalf("tags (-want +got):\n%s", d)
			}
			for _, e := range es {
				for _, s := range []string{bigClass, "p." + bigStruct, issueFunc, func32K, func100K} {
					if short(e.Name) == short(s) && e.Name != s || short(e.Parent) == short(s) && e.Parent != s {
						t.Errorf("%s: long string changed", e.Name)
					}
				}
			}
			if tc.path == "c.c" {
				if want := "(" + strings.Join(params, ",") + ")"; es[0].Signature != want {
					t.Errorf("signature: got %d bytes, want %d", len(es[0].Signature), len(want))
				}
			}
		})
	}
}

// Lines over the 1 MiB cap are skipped with no error and one Info line, and
// the stream stays in sync. Lines between 4 KiB and the cap are kept.
func TestParseLineLengthLimit(t *testing.T) {
	var info bytes.Buffer
	p := newTestParser(t, &info)

	kept := "K" + strings.Repeat("x", 512<<10)  // over the 4 KiB buffer, under the cap
	skipped := "S" + strings.Repeat("y", 2<<20) // over the cap
	src := "package p\n\nfunc Short() {}\n\nfunc " + kept + "() {}\n\nfunc " + skipped + "() {}\n\nfunc After() {}\n"
	es, err := p.Parse("a.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := []tag{
		{"p", 1, "", ""},
		{"Short", 3, "p", "package"},
		{short(kept), 5, "p", "package"},
		{"After", 9, "p", "package"},
	}
	if d := cmp.Diff(want, tags(es)); d != "" {
		t.Errorf("tags (-want +got):\n%s", d)
	}
	if n := strings.Count(info.String(), "\n"); n != 1 || !strings.Contains(info.String(), "skipping 1 long output lines") {
		t.Errorf("want one Info line about 1 skipped line, got %q", info.String())
	}

	info.Reset()
	es, err = p.Parse("b.go", []byte("package q\nfunc Next() {}\n"))
	if err != nil || len(es) != 2 || info.Len() != 0 {
		t.Errorf("next Parse: tags %v, err %v, info %q", tags(es), err, info.String())
	}
}

// N children of one parent keep one copy of its name, whether their lines fit
// in the read buffer or not. Long lines charge the shared name only once.
func TestParseSharedParent(t *testing.T) {
	for _, n := range []int{3000, 20000} {
		t.Run(fmt.Sprintf("%d B name", n), func(t *testing.T) {
			p := newTestParser(t, nil)
			// Charging the 20 KB parent to every field would cost 200 × 20 KB
			// = 4 MB. Shared, the file costs about 70 KB.
			setOutputBudget(p, 1, 256<<10)

			var b strings.Builder
			b.WriteString("package p\n\ntype T" + strings.Repeat("y", n) + " struct {\n")
			for i := 0; i < 200; i++ {
				fmt.Fprintf(&b, "\tF%d int\n", i)
			}
			b.WriteString("}\n")
			es, err := p.Parse("shared.go", []byte(b.String()))
			if err != nil {
				t.Fatal(err)
			}
			if len(es) != 202 {
				t.Fatalf("got %d tags, want 202 (package, struct, 200 fields)", len(es))
			}
			for _, e := range es[3:] {
				if unsafe.StringData(e.Parent) != unsafe.StringData(es[2].Parent) {
					t.Fatalf("%s: parent string is not shared", e.Name)
				}
			}
		})
	}
}

// Distinct long scopes cannot be shared: tags from long lines past the budget
// are dropped with no error and one Info line. Tags from short lines (which
// main returns) are always kept.
func TestParseOutputBudget(t *testing.T) {
	var info bytes.Buffer
	p := newTestParser(t, &info)
	setOutputBudget(p, 2, 0) // budget = 2 × the file size, about 45 KB

	// Each x has the distinct 20 KB scope "A<20 KB>.Bi".
	var b strings.Builder
	b.WriteString("class A" + strings.Repeat("y", 20000) + ":\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&b, "    class B%d:\n        x = 1\n", i)
	}
	b.WriteString("\nclass Small:\n    y = 1\n\ndef tail(): pass\n")

	es, err := p.Parse("budget.py", []byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range es {
		got[e.Name] = true
	}
	if !got["A"+strings.Repeat("y", 20000)] {
		t.Errorf("first long tag was dropped: %v", tags(es))
	}
	for _, n := range []string{"Small", "y", "tail"} {
		if !got[n] {
			t.Errorf("short tag %s was dropped", n)
		}
	}
	if len(es) >= 104 {
		t.Errorf("got all %d tags, want some dropped", len(es))
	}
	if n := strings.Count(info.String(), "\n"); n != 1 || !strings.Contains(info.String(), "long output lines") {
		t.Errorf("want one Info line about the budget, got %q", info.String())
	}
	t.Logf("kept %d of 104 tags; %s", len(es), info.String())

	es, err = p.Parse("b.go", []byte("package q\nfunc Next() {}\n"))
	if err != nil || len(es) != 2 {
		t.Errorf("next Parse: tags %v, err %v", tags(es), err)
	}
}

// Long lines that are not valid tags were skipped unread before the fix for
// issue #27. They must still be skipped, not fail Parse. So must "(null)"
// lines (issue #31).
func TestParseLongNonTagLines(t *testing.T) {
	var info bytes.Buffer
	p := newFakeParser(t, Options{Info: log.New(&info, "", 0)})
	long := strings.Repeat("x", 5000)
	reply := `{"_type": "tag", "name": "Before", "path": "a.go", "line": 1, "kind": "func"}
(null)
{"_type": "error", "message": "` + long + `", "fatal": true}
{"_type": "ptag", "name": "` + long + `"}
not json ` + long + `
{"_type": "tag", "scope": "Stale", "scopeKind": "class", "name": "` + long + `", "line": "1"}
{"_type": "tag", "name": "After", "path": "a.go", "line": 2, "kind": "func"}
{"_type": "completed", "command": "generate-tags"}
`
	for _, name := range []string{"a.go", "b.go"} {
		es, err := p.Parse(name, []byte(reply))
		if err != nil {
			t.Fatal(err)
		}
		want := []tag{{"Before", 1, "", ""}, {"After", 2, "", ""}}
		if d := cmp.Diff(want, tags(es)); d != "" {
			t.Fatalf("%s: tags (-want +got):\n%s", name, d)
		}
		if want := "ctags skipping 4 long output lines for " + name + "\n"; info.String() != want {
			t.Errorf("info: got %q, want %q", info.String(), want)
		}
		info.Reset()
	}
}

// Once a long tag does not fit in the budget, every later long line of that
// Parse is skipped, even one that would still fit. Short lines are kept, and
// the next Parse starts with a fresh budget.
func TestParseOutputBudgetStops(t *testing.T) {
	var info bytes.Buffer
	p := newFakeParser(t, Options{Info: log.New(&info, "", 0)})
	setOutputBudget(p, 0, 10000) // room for one 5 KB line, not two

	longTag := func(i int) string {
		return fmt.Sprintf(`{"_type": "tag", "name": "x%d", "line": %d, "scope": "S%d%s"}`+"\n", i, i+1, i, strings.Repeat("y", 5000))
	}
	// A tag line of exactly 4,096 bytes, the shortest long line. It would
	// fit in what is left of the budget after x0.
	edge := `{"_type": "tag", "line": 9, "name": "e`
	edge += strings.Repeat("e", 4096-len(edge)-2) + `"}` + "\n"
	completed := `{"_type": "completed", "command": "generate-tags"}` + "\n"

	es, err := p.Parse("a.go", []byte(longTag(0)+longTag(1)+edge+longTag(2)+`{"_type": "tag", "name": "short", "line": 4}`+"\n"+completed))
	if err != nil {
		t.Fatal(err)
	}
	if d := cmp.Diff([]tag{{"x0", 1, short("S0" + strings.Repeat("y", 5000)), ""}, {"short", 4, "", ""}}, tags(es)); d != "" {
		t.Fatalf("tags (-want +got):\n%s", d)
	}
	if want := "ctags skipping 3 long output lines for a.go\n"; info.String() != want {
		t.Errorf("info: got %q, want %q", info.String(), want)
	}

	es, err = p.Parse("b.go", []byte(longTag(3)+completed))
	if err != nil || len(es) != 1 {
		t.Errorf("next Parse: tags %v, err %v", tags(es), err)
	}
}

// When the output ends in the middle of a long line, for example because ctags
// was killed, Parse must return an error as it did before, not loop forever.
func TestParseEOFInLongLine(t *testing.T) {
	// Answer the first request with half of a long line, then exit.
	bin := writeFakeScript(t, `echo '{"_type": "program", "name": "Universal Ctags"}'`+"\n"+
		"read -r request\n"+
		"printf '%s' '"+strings.Repeat("x", 5000)+"'\n")
	p, err := New(Options{Bin: bin})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	done := make(chan error, 1)
	go func() {
		_, err := p.Parse("a.go", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Parse did not return after ctags exited")
	}
}

// Long lines printed while ctags starts were skipped unread before the fix
// for issue #27. They must not fail New.
func TestNewLongInitLine(t *testing.T) {
	bin := writeFakeScript(t, `echo '{"_type": "error", "message": "`+strings.Repeat("m", 5000)+`", "fatal": true}'`+"\n"+
		`echo '{"_type": "program", "name": "Universal Ctags"}'`+"\n"+
		"cat >/dev/null\n")
	p, err := New(Options{Bin: bin})
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
}

// writeFakeScript writes a shell script that stands in for ctags, for replies
// that fakeCtags cannot script, such as output during startup.
func writeFakeScript(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ctags")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// TestCloseRemovesTempDir checks that a killed ctags leaves nothing in
// TMPDIR once the parser is closed (issue #29).
func TestCloseRemovesTempDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	p, err := New(Options{Bin: os.Getenv("CTAGS_COMMAND")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)

	// Each field repeats the long struct name in its scope, so ctags spends a
	// while writing a large reply to its temporary file.
	var b strings.Builder
	b.WriteString("package a\ntype " + strings.Repeat("A", 4000) + " struct {\n")
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&b, "F%d int\n", i)
	}
	b.WriteString("}\n")
	done := make(chan struct{})
	go func() {
		_, _ = p.Parse("a.go", []byte(b.String()))
		close(done)
	}()

	// Close the parser while ctags writes, as Zoekt does when Parse times out.
	// Parse then calls Close too, concurrently (issue #28).
	for {
		// Any file in a directory under TMPDIR: ctags has started writing.
		if files, _ := filepath.Glob(filepath.Join(tmp, "*", "*")); len(files) > 0 {
			break
		}
		select {
		case <-done:
			t.Fatal("Parse finished before ctags created a temporary file")
		case <-time.After(time.Millisecond):
		}
	}
	p.Close()
	<-done

	if entries, err := os.ReadDir(tmp); err != nil || len(entries) > 0 {
		t.Fatalf("expected TMPDIR to be empty, got %v (err %v)", entries, err)
	}
}

// TestNewFailureLeavesNoTempDir checks that New cleans up after itself when
// ctags cannot start.
func TestNewFailureLeavesNoTempDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	if _, err := New(Options{Bin: filepath.Join(tmp, "missing-ctags")}); err == nil {
		t.Fatal("expected an error")
	}
	if entries, err := os.ReadDir(tmp); err != nil || len(entries) > 0 {
		t.Fatalf("expected TMPDIR to be empty, got %v (err %v)", entries, err)
	}
}
