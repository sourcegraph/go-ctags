package ctags

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	input := strings.Join([]string{
		"aaaaaaaaa",
		strings.Repeat("B", 3*size+3),
		strings.Repeat("C", size) + strings.Repeat("D", size+1),
		"",
		strings.Repeat("e", size-1),
		"f\r",
		"gg",
	}, "\n")
	want := []string{
		"aaaaaaaaa",
		strings.Repeat("e", size-1),
		"f",
		"gg",
	}

	var got []string
	r := &scanner{r: bufio.NewReaderSize(strings.NewReader(input), size)}
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

	// Kill ctags while it writes, as a timeout or the OOM killer would. Killing
	// the process directly avoids calling Close while Parse runs.
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
	_ = p.(*ctagsProcess).cmd.Process.Kill()
	<-done
	p.Close()

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
