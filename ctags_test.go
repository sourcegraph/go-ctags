package ctags

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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
		want []*Entry
	}

	cases := []tc{{
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
		want: []*Entry{
			{
				Kind:     "package",
				Language: "Java",
				Line:     2,
				Name:     "com.sourcegraph",
				Path:     "com/sourcegraph/A.java",
			},
			{
				Kind:     "class",
				Language: "Java",
				Line:     4,
				Name:     "A",
				Path:     "com/sourcegraph/A.java",
			},

			{
				Kind:       "field",
				Language:   "Java",
				Line:       5,
				Name:       "D",
				Parent:     "A",
				ParentKind: "class",
				Path:       "com/sourcegraph/A.java",
			},
			{
				Kind:       "field",
				Language:   "Java",
				Line:       6,
				Name:       "E",
				Parent:     "A",
				ParentKind: "class",
				Path:       "com/sourcegraph/A.java",
			},
			{
				Kind:       "method",
				Language:   "Java",
				Line:       7,
				Name:       "A",
				Parent:     "A",
				ParentKind: "class",
				Path:       "com/sourcegraph/A.java",
				Signature:  "()",
			},
			{
				Kind:       "method",
				Language:   "Java",
				Line:       10,
				Name:       "F",
				Parent:     "A",
				ParentKind: "class",
				Path:       "com/sourcegraph/A.java",
				Signature:  "()",
			},
		}}, {
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
		want: []*Entry{
			{
				Name:     "query",
				Path:     "schema.graphql",
				Line:     3,
				Kind:     "field",
				Language: "GraphQL",
			},
			{
				Name:     "mutation",
				Path:     "schema.graphql",
				Line:     4,
				Kind:     "field",
				Language: "GraphQL",
			},
			{
				Name:     "Node",
				Path:     "schema.graphql",
				Line:     9,
				Kind:     "interface",
				Language: "GraphQL",
			},
			{
				Name:     "id",
				Path:     "schema.graphql",
				Line:     13,
				Kind:     "field",
				Language: "GraphQL",
			},
		},
	}}

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

		if d := cmp.Diff(tc.want, got, cmpopts.IgnoreFields(Entry{}, "Pattern"), cmpopts.EquateEmpty()); d != "" {
			t.Errorf("%s mismatch (-want +got):\n%s", tc.path, d)
		}
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
