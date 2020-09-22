package ctags

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestParser(t *testing.T) {
	p, err := New(Options{
		Bin: os.Getenv("CTAGS_COMMAND"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	cases := []struct {
		path string
		data string
		want []*Entry
	}{{
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

	for _, tc := range cases {
		got, err := p.Parse(tc.path, []byte(tc.data))
		if err != nil {
			t.Error(err)
		}

		if d := cmp.Diff(tc.want, got, cmpopts.IgnoreFields(Entry{}, "Pattern")); d != "" {
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
