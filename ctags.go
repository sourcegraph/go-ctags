// Package ctags provides a Go wrapper for universal-ctags.
package ctags

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"strings"
	"sync"
	"unicode/utf8"
)

type Entry struct {
	Name       string
	Path       string
	Line       int
	Kind       string
	Language   string
	Parent     string
	ParentKind string
	Pattern    string
	Signature  string

	FileLimited bool
}

type Parser interface {
	Parse(path string, content []byte) ([]*Entry, error)
	Close()
}

type Options struct {
	// Bin is the command to run. Defaults to "universal-ctags" if empty.
	Bin string

	// PatternLengthLimit is the cutoff length of the patterns output by
	// ctags. (--pattern-length-limit). If 0 defaults to 255.
	//
	// Note: --pattern-length-limit=0 disables this in universal-ctags. We don't
	// allow disabling it.
	PatternLengthLimit int

	// Info if non-nil will log info messages
	Info *log.Logger

	// Debug if non-nil will log debug messages
	Debug *log.Logger
}

func New(opts Options) (Parser, error) {
	if opts.Bin == "" {
		opts.Bin = "universal-ctags"
	}
	if opts.PatternLengthLimit == 0 {
		opts.PatternLengthLimit = 255
	}

	// ctagsArgs is the contents of ctags.d. ctags.d needs to be in a specific
	// location to be read correctly. We have accidently regressed on this
	// twice. Instead we pass in the arguments here.
	args := []string{"--_interactive=default", "--fields=*", fmt.Sprintf("--pattern-length-limit=%d", opts.PatternLengthLimit)}
	args = append(args, ctagsArgs...)

	// Some languages cause issues in universal-ctags. Stick to an allowlist of
	// known working languages.
	args = append(args, "--languages="+strings.Join(SupportedLanguages[:], ","))

	// ctags writes each reply to a temporary file before printing it. If the
	// process is killed, for example by Close during a slow Parse, that file
	// stays behind. Give ctags its own directory so that Close can remove it.
	// ctags looks the directory up again for every request and crashes if it
	// is gone, so it must live as long as the process.
	tmpDir, err := os.MkdirTemp("", "go-ctags-")
	if err != nil {
		return nil, fmt.Errorf("creating temporary directory for %s: %w", opts.Bin, err)
	}

	cmd := exec.Command(opts.Bin, args...)
	// ctags reads TMPDIR, or TMP on Windows. It never reads TEMP.
	cmd.Env = append(os.Environ(), "TMPDIR="+tmpDir, "TMP="+tmpDir)
	in, err := cmd.StdinPipe()
	if err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, err
	}

	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		_ = os.RemoveAll(tmpDir)
		return nil, err
	}
	cmd.Stderr = os.Stderr
	proc := ctagsProcess{
		cmd:     cmd,
		in:      in,
		out:     &scanner{r: bufio.NewReaderSize(out, 4096), maxLen: maxLineSize},
		outPipe: out,
		tmpDir:  tmpDir,

		ratio:     outputRatioLimit,
		minBudget: maxLineSize,

		Info:  opts.Info,
		Debug: opts.Debug,
	}

	if err := cmd.Start(); err != nil {
		_ = os.RemoveAll(tmpDir)
		return nil, err
	}

	// Need to check for fatal errors. Final init.Typ for initialization should
	// be "program" but we stop at the first non error.
	var init reply
	for {
		if err := proc.read(&init); err != nil {
			proc.Close()
			return nil, err
		}

		if init.Typ != "error" {
			break
		}

		if init.Fatal {
			proc.Close()
			return nil, fmt.Errorf("starting %s failed with: %s", opts.Bin, init.Message)
		} else {
			if opts.Info != nil {
				opts.Info.Printf("ignoring ctags initialization warning: %s", init.Message)
			}
			continue
		}
	}

	return &proc, nil
}

type ctagsProcess struct {
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     *scanner
	outPipe io.ReadCloser
	tmpDir  string // ctags' private TMPDIR, removed by Close

	// ratio and minBudget set the budget of each Parse: at most
	// max(minBudget, ratio*len(content)) bytes kept from lines that do not
	// fit in the read buffer. New sets them to outputRatioLimit and
	// maxLineSize. They are fields so that tests can lower them.
	ratio, minBudget int

	Info  *log.Logger
	Debug *log.Logger

	closeOnce sync.Once
}

// Close kills ctags and cleans up after it. It is safe to call more than once
// and concurrently with Parse: Zoekt closes a parser whose Parse timed out,
// and the read in that Parse then calls Close as well. Every call returns
// only once ctags has exited.
func (p *ctagsProcess) Close() {
	p.closeOnce.Do(p.close)
}

func (p *ctagsProcess) close() {
	_ = p.cmd.Process.Kill()
	_ = p.outPipe.Close()
	_ = p.in.Close()
	_ = p.cmd.Wait()
	// Remove the directory only once ctags has exited, so that ctags cannot
	// create a file in it afterwards.
	_ = os.RemoveAll(p.tmpDir)
}

func (p *ctagsProcess) read(rep *reply) error {
	for {
		if !p.out.Scan() {
			// Some errors do not kill the parser. We would deadlock if we waited
			// for the process to exit.
			err := p.out.Err()
			p.Close()
			return err
		}
		if p.Debug != nil {
			p.Debug.Printf("read %q", p.out.Bytes())
		}

		// Old ctags versions could print "(null)" lines. Skip them; returning
		// an empty reply would end Parse early and leave the rest of the reply
		// to the next Parse. See
		// https://github.com/universal-ctags/ctags/issues/1493
		if bytes.Equal([]byte("(null)"), p.out.Bytes()) {
			continue
		}

		err := json.Unmarshal(p.out.Bytes(), rep)
		if p.out.long && (err != nil || rep.Typ != "tag") {
			// Skip long lines that are not tags: before issue #27 every long
			// line was skipped, so callers never got an error for one. Clear
			// rep, which Unmarshal may have filled in part.
			*rep = reply{}
			p.out.skipped++
			continue
		}
		if err != nil {
			// Quote at most 200 bytes: a line can be up to maxLineSize.
			return fmt.Errorf("unmarshal(%.200q): %v", p.out.Bytes(), err)
		}
		return nil
	}
}

// universal-ctags line buffer size is only 1024.
const ctagsLineBufferSize = 1024

func (p *ctagsProcess) post(req *request, content []byte) (bool, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return false, err
	}
	body = append(body, '\n')

	// -1 for c-style string
	if len(body) > ctagsLineBufferSize-1 {
		return false, nil
	}

	if p.Debug != nil {
		p.Debug.Printf("post %q", body)
	}

	if _, err = p.in.Write(body); err != nil {
		return false, err
	}

	_, err = p.in.Write(content)
	if p.Debug != nil {
		p.Debug.Println(string(content))
	}
	return err == nil, err
}

type request struct {
	Command  string `json:"command"`
	Filename string `json:"filename"`
	Size     int    `json:"size"`
}

type reply struct {
	// Init
	Typ     string `json:"_type"`
	Name    string `json:"name"`
	Version string `json:"version"`

	// completed
	Command string `json:"command"`

	// error
	Message string `json:"message"`
	Fatal   bool   `json:"fatal"`

	Path      string `json:"path"`
	Language  string `json:"language"`
	Line      int    `json:"line"`
	Kind      string `json:"kind"`
	End       int    `json:"end"`
	Scope     string `json:"scope"`
	ScopeKind string `json:"scopeKind"`
	Access    string `json:"access"`
	File      bool   `json:"file"`
	Signature string `json:"signature"`
	Pattern   string `json:"pattern"`
}

func (p *ctagsProcess) Parse(name string, content []byte) ([]*Entry, error) {
	filename := path.Base(name)
	req := request{
		Command:  "generate-tags",
		Size:     len(content),
		Filename: filename,
	}

	if !utf8.Valid(content) {
		if p.Info != nil {
			p.Info.Printf("ctags skipping file due not being utf-8 encoded: %s", name)
		}
		return nil, nil
	}

	if ok, err := p.post(&req, content); err != nil {
		return nil, err
	} else if !ok {
		if p.Info != nil {
			p.Info.Printf("ctags skipping file due to long filename: %s", name)
		}
		return nil, nil
	}

	// Every child tag repeats the full name of its parent in scope, so tags
	// can be much larger than the file. Share parent strings between tags,
	// and bound the bytes kept from lines that do not fit in the read buffer.
	// Only those lines count against the budget: shorter lines were always
	// kept, so Parse still returns every tag it did before. Once a tag does
	// not fit in the budget, every later long line in this Parse is skipped.
	budget := max(p.minBudget, p.ratio*len(content))
	used, dropped := 0, 0
	parents := map[string]string{}
	p.out.maxLen, p.out.skipped = maxLineSize, 0

	// 250 is a better guess for initial size
	es := make([]*Entry, 0, 250)
	for {
		var rep reply
		if err := p.read(&rep); err != nil {
			return nil, err
		}
		switch rep.Typ {
		case "completed":
			if n := p.out.skipped + dropped; n > 0 && p.Info != nil {
				p.Info.Printf("ctags skipping %d long output lines for %s", n, name)
			}
			return es, nil
		case "error":
			if rep.Fatal {
				// ctags carries on after a fatal error in interactive mode, so
				// the rest of this reply is still in the pipe. Close the parser
				// so that later calls fail instead of reading it as their own.
				p.Close()
				return nil, fmt.Errorf("fatal ctags error for %s: %s", name, rep.Message)
			} else if p.Info != nil {
				p.Info.Printf("ignoring non-fatal ctags error for %s: %s", name, rep.Message)
			}
		case "tag":
			// Short scopes are not worth a map entry: sharing them saves
			// little and costs more allocations on every ordinary file.
			intern := len(rep.Scope) >= minSharedParentLen
			var parent string
			shared := false
			if intern {
				parent, shared = parents[rep.Scope]
			}
			if p.out.long {
				// A shared parent costs nothing more: it is kept once.
				cost := len(p.out.Bytes()) - len(parent)
				if cost > budget-used {
					// The budget is spent. Lower the scanner's limit below
					// the buffer size so that it skips every later long line
					// without decoding it. Parse resets it on the next call.
					p.out.maxLen = p.out.r.Size() - 1
					dropped++
					continue
				}
				used += cost
			}
			if shared {
				rep.Scope = parent
			} else if intern {
				parents[rep.Scope] = rep.Scope
			}
			if rep.Path == filename {
				rep.Path = name
			}
			es = append(es, &Entry{
				Name:       rep.Name,
				Path:       rep.Path,
				Line:       rep.Line,
				Kind:       rep.Kind,
				Language:   rep.Language,
				Parent:     rep.Scope,
				ParentKind: rep.ScopeKind,
				Pattern:    rep.Pattern,
				Signature:  rep.Signature,
			})
		default:
			return nil, fmt.Errorf("ctags unexpected response %s for %s", rep.Typ, name)
		}
	}
}

const (
	// maxLineSize is the longest ctags output line we read. Longer lines,
	// and so the tags they describe, are skipped.
	maxLineSize = 1 << 20

	// minSharedParentLen is the shortest parent name that Parse shares
	// between the tags of a file.
	minSharedParentLen = 256

	// outputRatioLimit bounds the bytes kept per Parse from lines that do not
	// fit in the read buffer to this many times the size of the content (but
	// at least maxLineSize). Zoekt holds every file's tags in memory until
	// the shard is written, so this stays low.
	outputRatioLimit = 10
)

// scanner is like bufio.Scanner but skips lines longer than maxLen instead of
// returning bufio.ErrTooLong. Lines that do not fit in r's buffer are copied
// into a reused buffer, and Scan sets long for them.
//
// Additionally it will skip empty lines.
type scanner struct {
	r      *bufio.Reader
	maxLen int // excluding the trailing newline, including any \r before it
	buf    []byte
	line   []byte
	long   bool // line did not fit in r's buffer
	err    error

	skipped int // long lines skipped: longer than maxLen, or not tags (see read)
}

func (s *scanner) Scan() bool {
	for s.err == nil {
		line, err := s.r.ReadSlice('\n')
		s.long = err == bufio.ErrBufferFull
		if s.long {
			line, err = s.readLong(line)
		}
		s.err = err

		line = bytes.TrimSuffix(line, []byte{'\n'})
		if len(line) > s.maxLen {
			s.skipped++
			continue
		}
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(line) > 0 {
			s.line = line
			return true
		}
	}
	s.line, s.long = nil, false
	return false
}

// readLong reads the rest of a line that did not fit in r's buffer. It stops
// copying once the line is longer than maxLen (the result is then cut, and
// Scan skips it), but still consumes the whole line so that the next Scan
// starts on a line boundary.
func (s *scanner) readLong(line []byte) ([]byte, error) {
	s.buf = append(s.buf[:0], line...)
	for {
		line, err := s.r.ReadSlice('\n')
		if len(s.buf) <= s.maxLen+1 {
			s.buf = append(s.buf, line...)
		}
		if err != bufio.ErrBufferFull {
			return s.buf, err
		}
	}
}

func (s *scanner) Bytes() []byte {
	return s.line
}

func (s *scanner) Err() error {
	if s.err == io.EOF {
		return nil
	}
	return s.err
}
