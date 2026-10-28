package ctags

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
)

// TestMain lets the test binary stand in for universal-ctags when
// GO_CTAGS_FAKE is set. See fakeCtags.
func TestMain(m *testing.M) {
	if os.Getenv("GO_CTAGS_FAKE") != "" {
		fakeCtags()
		return
	}
	os.Exit(m.Run())
}

// fakeCtags speaks the interactive protocol, but replies to each
// generate-tags request with the request's content as is. This lets tests
// script the exact reply lines ctags would print.
func fakeCtags() {
	fmt.Println(`{"_type": "program", "name": "Universal Ctags"}`)
	r := bufio.NewReader(os.Stdin)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			return
		}
		if _, err := io.CopyN(os.Stdout, r, int64(req.Size)); err != nil {
			return
		}
	}
}

// newFakeParser returns a Parser backed by fakeCtags.
func newFakeParser(t *testing.T, opts Options) Parser {
	t.Setenv("GO_CTAGS_FAKE", "1")
	opts.Bin = os.Args[0]
	p, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}
