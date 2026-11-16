package main

// Helpers that wrap the bundled utl-x CLI: locating the binary, preparing a
// scratch directory, writing inputs, synthesising script headers and running
// the process with a deadline.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	sdk "github.com/SolaceDev/solace-agent-mesh-go/pkg/samtoolsdk"
)

// formatInfo describes one utl-x format: the value for --from/--to and the
// script header, the file extension used for outputs, and the MIME type.
type formatInfo struct {
	ext  string
	mime string
	kind string // "data" or "schema"
}

var formats = map[string]formatInfo{
	"json":  {".json", "application/json", "data"},
	"xml":   {".xml", "application/xml", "data"},
	"csv":   {".csv", "text/csv", "data"},
	"yaml":  {".yaml", "application/yaml", "data"},
	"odata": {".json", "application/json", "data"},
	"xsd":   {".xsd", "application/xml", "schema"},
	"jsch":  {".schema.json", "application/schema+json", "schema"},
	"avro":  {".avsc", "application/json", "schema"},
	"proto": {".proto", "text/plain", "schema"},
	"osch":  {".edmx", "application/xml", "schema"},
	"tsch":  {".tableschema.json", "application/json", "schema"},
}

// formatAliases maps common spellings to the canonical utl-x format name.
var formatAliases = map[string]string{
	"yml":          "yaml",
	"jsonschema":   "jsch",
	"json-schema":  "jsch",
	"json_schema":  "jsch",
	"avsc":         "avro",
	"protobuf":     "proto",
	"edmx":         "osch",
	"tableschema":  "tsch",
	"table-schema": "tsch",
}

// normalizeFormat returns the canonical format name, or an error listing the
// valid values. allowAuto permits "auto" (input side only).
func normalizeFormat(f string, allowAuto bool) (string, error) {
	f = strings.ToLower(strings.TrimSpace(f))
	if a, ok := formatAliases[f]; ok {
		f = a
	}
	if f == "auto" && allowAuto {
		return f, nil
	}
	if _, ok := formats[f]; ok {
		return f, nil
	}
	return "", fmt.Errorf("unknown format %q; use one of: json, xml, csv, yaml, odata (data) or xsd, jsch, avro, proto, osch, tsch (schemas)", f)
}

// formatFromFilename guesses a format from a file extension; "" if unknown.
func formatFromFilename(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, ".schema.json"):
		return "jsch"
	case strings.HasSuffix(n, ".edmx"):
		return "osch"
	}
	switch filepath.Ext(n) {
	case ".json":
		return "json"
	case ".xml":
		return "xml"
	case ".csv", ".tsv":
		return "csv"
	case ".yaml", ".yml":
		return "yaml"
	case ".xsd":
		return "xsd"
	case ".avsc":
		return "avro"
	case ".proto":
		return "proto"
	}
	return ""
}

// bundledNames are the file names build.sh/build.bat give the bundled CLI.
func bundledNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"utlx.exe", "utlx"}
	}
	return []string{"utlx"}
}

// locateUtlx finds the utl-x binary: tool config "utlx_path", then bin/utlx
// (bin\utlx.exe on Windows) next to this tool binary, then $PATH.
func locateUtlx(tc *sdk.ToolContext) (string, error) {
	if tc != nil {
		if p := strings.TrimSpace(tc.GetConfigString("utlx_path", "")); p != "" {
			return p, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		for _, n := range bundledNames() {
			cand := filepath.Join(filepath.Dir(exe), "bin", n)
			if st, err := os.Stat(cand); err == nil && !st.IsDir() {
				return cand, nil
			}
		}
	}
	if p, err := exec.LookPath("utlx"); err == nil {
		return p, nil
	}
	return "", errors.New("utl-x binary not found: expected bin/utlx (bin\\utlx.exe on Windows) next to the tool binary, on PATH, or set the utlx_path tool config")
}

// workspace is a per-call scratch directory under the sandbox TMPDIR.
type workspace struct{ dir string }

func newWorkspace() (*workspace, error) {
	d, err := os.MkdirTemp("", "utlx-")
	if err != nil {
		return nil, err
	}
	return &workspace{dir: d}, nil
}

func (w *workspace) close() { _ = os.RemoveAll(w.dir) }

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// write stores content under a sanitised file name and returns its path.
func (w *workspace) write(name string, content []byte) (string, error) {
	base := unsafeName.ReplaceAllString(filepath.Base(name), "_")
	if base == "" || base == "." || base == ".." {
		base = "file"
	}
	p := filepath.Join(w.dir, base)
	// Avoid collisions between inputs that share a name.
	for i := 2; ; i++ {
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			break
		}
		ext := filepath.Ext(base)
		p = filepath.Join(w.dir, fmt.Sprintf("%s_%d%s", strings.TrimSuffix(base, ext), i, ext))
	}
	return p, os.WriteFile(p, content, 0o600)
}

var identRe = regexp.MustCompile(`[^A-Za-z0-9_]+`)

// inputName derives a valid utl-x identifier from a file name ("orders.xml" -> "orders").
func inputName(filename string) string {
	stem := filepath.Base(filename)
	if i := strings.Index(stem, "."); i > 0 {
		stem = stem[:i]
	}
	n := strings.Trim(identRe.ReplaceAllString(stem, "_"), "_")
	if n == "" {
		n = "input"
	}
	if n[0] >= '0' && n[0] <= '9' {
		n = "in_" + n
	}
	return n
}

// hasHeader reports whether a script already carries the %utlx header.
func hasHeader(script string) bool {
	return strings.HasPrefix(strings.TrimSpace(script), "%utlx")
}

// namedInput is one input of a multi-input transformation.
type namedInput struct {
	name, format, path string
}

// buildScript returns a complete script. A script that already starts with
// %utlx is returned unchanged; a body-only script gets a header built from
// the given input and output formats.
func buildScript(script string, inputs []namedInput, inFormat, outFormat string) string {
	if hasHeader(script) {
		return script
	}
	var b strings.Builder
	b.WriteString("%utlx 1.0\n")
	switch {
	case len(inputs) > 1:
		parts := make([]string, len(inputs))
		for i, in := range inputs {
			parts[i] = in.name + " " + in.format
		}
		b.WriteString("input: " + strings.Join(parts, ", ") + "\n")
	default:
		b.WriteString("input " + inFormat + "\n")
	}
	b.WriteString("output " + outFormat + "\n---\n")
	b.WriteString(strings.TrimSpace(script))
	b.WriteString("\n")
	return b.String()
}

var outputHeaderRe = regexp.MustCompile(`(?m)^\s*output\s*:?\s*([A-Za-z_]+)(?:\s+([A-Za-z_]+))?`)

// scriptOutputFormat reads the output format from a script header ("" if unknown).
func scriptOutputFormat(script string) string {
	head := script
	if i := strings.Index(script, "\n---"); i >= 0 {
		head = script[:i]
	}
	m := outputHeaderRe.FindStringSubmatch(head)
	if m == nil {
		return ""
	}
	for _, g := range m[1:] {
		if f, err := normalizeFormat(g, false); err == nil {
			return f
		}
	}
	return ""
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// runResult is the outcome of one utl-x invocation.
type runResult struct {
	stdout, stderr string
	exitCode       int
}

// runUtlx executes the binary with a deadline. A non-zero exit is reported in
// runResult, not as an error; error means the process could not run at all.
func runUtlx(ctx context.Context, bin, dir string, timeout time.Duration, args ...string) (runResult, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := runResult{
		stdout: ansiRe.ReplaceAllString(out.String(), ""),
		stderr: ansiRe.ReplaceAllString(errb.String(), ""),
	}
	if cctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("utl-x timed out after %s", timeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.exitCode = ee.ExitCode()
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("run utl-x: %w", err)
	}
	return res, nil
}

// clip shortens long text for the LLM and reports whether it was cut.
func clip(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	return s[:max] + "\n… [truncated]", true
}

// errorText picks the most useful diagnostics from a failed run.
func errorText(r runResult) string {
	msg := strings.TrimSpace(r.stderr)
	if msg == "" {
		msg = strings.TrimSpace(r.stdout)
	}
	if msg == "" {
		msg = fmt.Sprintf("utl-x exited with code %d and no output", r.exitCode)
	}
	t, _ := clip(msg, 6000)
	return t
}

// outputName builds the artifact name for a result: an explicit name wins,
// otherwise the input's stem plus the extension of the output format.
func outputName(explicit *string, basis, format string) string {
	if explicit != nil && strings.TrimSpace(*explicit) != "" {
		return filepath.Base(strings.TrimSpace(*explicit))
	}
	stem := "output"
	if basis != "" {
		stem = filepath.Base(basis)
		if i := strings.Index(stem, "."); i > 0 {
			stem = stem[:i]
		}
	}
	ext := ".txt"
	if fi, ok := formats[format]; ok {
		ext = fi.ext
	}
	return stem + ext
}

func mimeFor(format string) string {
	if fi, ok := formats[format]; ok {
		return fi.mime
	}
	return "text/plain"
}
