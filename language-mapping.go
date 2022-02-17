package ctags

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

var SupportedLanguages = [...]string{"Basic", "C", "C#", "C++", "Clojure", "Cobol", "CSS", "CUDA", "D", "Elixir", "elm", "Erlang", "Go", "GraphQL", "Groovy", "haskell", "Java", "JavaScript", "Jsonnet", "kotlin", "Lisp", "Lua", "MatLab", "ObjectiveC", "OCaml", "Pascal", "Perl", "Perl6", "PHP", "Powershell", "Protobuf", "Python", "R", "Ruby", "Rust", "scala", "Scheme", "Sh", "swift", "SystemVerilog", "Tcl", "Thrift", "typescript", "tsx", "Verilog", "VHDL", "Vim"}

func ListLanguageMappings(ctx context.Context, bin string) (map[string][]string, error) {
	if bin == "" {
		bin = "universal-ctags"
	}

	args := make([]string, 0, len(ctagsArgs)+2)
	args = append(args, ctagsArgs...)
	args = append(args, "--languages="+strings.Join(SupportedLanguages[:], ","))
	args = append(args, "--list-maps")

	var (
		stderr bytes.Buffer
		stdout bytes.Buffer
	)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("running %s failed with exit code %d: %v", bin, cmd.ProcessState.ExitCode(), stderr.String())
		}
		return nil, fmt.Errorf("failed to start %s: %v", bin, err)
	}

	lines := strings.Split(stdout.String(), "\n")
	mapping := make(map[string][]string, len(lines))
	for _, line := range lines {
		split := strings.SplitN(line, " ", 2)
		if len(split) != 2 {
			continue
		}
		mapping[split[0]] = strings.Fields(split[1])
	}

	return mapping, nil
}
