// Command utlx is a Solace Agent Mesh toolset that lets agents create and run
// UTL-X transformers (https://github.com/grauwen/utl-x) between any formats
// utl-x supports: data (JSON, XML, CSV, YAML, OData) and schemas (XSD, JSON
// Schema, Avro, Protobuf, OData/EDMX, Table Schema).
//
// The tools shell out to the utl-x CLI, which build.sh bundles as bin/utlx
// next to this binary.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/SolaceDev/solace-agent-mesh-go/pkg/samtoolsdk"
)

const previewChars = 2000

// instructions is injected into the agent's system prompt once (it is attached
// to utlx_transform only, so it is not repeated per tool).
const instructions = `UTL-X tools (utlx_*) transform data and schemas between formats.
Data formats: json, xml, csv, yaml, odata. Schema formats: xsd, jsch (JSON Schema), avro, proto (Protobuf), osch (OData/EDMX), tsch (Table Schema).

Pick the tool:
- Pure format change, no mapping (XML->JSON, CSV->YAML, XSD->JSON Schema/Avro/Protobuf): utlx_convert.
- Mapping, filtering, renaming, aggregating, joining: utlx_transform with a UTL-X script.
- A reusable transformer the user wants to keep: utlx_create_transformer (validates, test-runs on a sample, saves <name>.utlx). Run a saved one later with utlx_transform(transformer=<name>.utlx).
- Unsure about a function name or signature: utlx_functions before writing the script. The stdlib has ~700 functions.

UTL-X script shape:
  %utlx 1.0
  input xml                       (or: input csv {headers: true, delimiter: ";"})
  output json                     (or: output csv {delimiter: ";"}, output xml {encoding: "UTF-8"})
  ---
  { id: $input.Order.@id, items: $input.Order.Item |> map(i => {sku: i.@sku, qty: parseNumber(i.@qty)}) }
You may pass only the body (after ---) and set input_format/output_format; the tool adds the header.
Syntax: $input is the data; .field navigates; .@attr is an XML attribute; ..name is recursive descent; |> pipes; lambdas are x => expr;
  if (c) a else b; match v { "a" => 1, _ => 0 }; let x = expr; safe navigation a?.b ?? "default".
Common functions: map, filter, reduce, sortBy, groupBy, count, sum, avg, first, take, distinct, flatten, keys, values,
  upper, lower, trim, split, join, replace, substring, parseNumber, toString, now, formatDate, parseDate, addDays.
Multiple inputs: header "input: orders json, customers csv", reference them as $orders and $customers; pass the files in "inputs" (names come from input_names or the file stems).
XML attributes appear as "@name" keys when converted to JSON/YAML. XML attribute and element values are strings: parseNumber them before arithmetic.
Joining across inputs: normalise both keys before comparing (toString(a.id) == toString(b.id)); numbers from different inputs/formats may not compare equal otherwise.
On an error, read the message (it has line:column), fix the script and retry. Never invent output: report the tool error if it keeps failing.`

func main() { sdk.Run(tools()...) }

// tools returns the tool definitions (shared by main and the tests).
func tools() []*sdk.ToolDef {
	cfg := []sdk.ConfigSchemaField{
		{Key: "utlx_path", Type: "string", Description: "Path to the utl-x binary. Leave empty to use the bundled bin/utlx."},
		{Key: "max_runtime_seconds", Type: "integer", Description: "Kill a utl-x run after this many seconds.", Default: 100},
	}
	return []*sdk.ToolDef{
		sdk.NewTool("utlx_transform",
			"Run a UTL-X transformation: map, filter, restructure or join data and schemas between JSON, XML, CSV, YAML, OData, XSD, JSON Schema, Avro, Protobuf, OData/EDMX and Table Schema. Takes a UTL-X script (or a saved .utlx transformer artifact) and one input artifact, inline input text, or several named inputs. Saves the result as an artifact and returns a preview.",
			transform,
			sdk.WithInstructions(instructions), sdk.WithConfigSchema(cfg...), sdk.WithTimeout(120)),
		sdk.NewTool("utlx_convert",
			"Convert a file from one format to another without a mapping script (structure is kept): e.g. XML to JSON or YAML, CSV to JSON, JSON to XML, XSD to JSON Schema, Avro or Protobuf, JSON Schema to XSD. Saves the result as an artifact and returns a preview.",
			convert,
			sdk.WithConfigSchema(cfg...), sdk.WithTimeout(120)),
		sdk.NewTool("utlx_create_transformer",
			"Create a reusable UTL-X transformer: validates the script, optionally test-runs it on a sample input, and saves it as <name>.utlx so it can be run later with utlx_transform. Returns the validation result and the sample output preview, or the errors to fix.",
			createTransformer,
			sdk.WithConfigSchema(cfg...), sdk.WithTimeout(120)),
		sdk.NewTool("utlx_functions",
			"Look up UTL-X standard library functions: pass function for the signature, parameters and examples of one function; query to search by keyword (e.g. 'date', 'xml', 'group'); category to list a category (e.g. String, Array, Date, XML, CSV); nothing for category statistics.",
			functions,
			sdk.WithConfigSchema(cfg...), sdk.WithTimeout(60)),
		sdk.NewTool("utlx_map",
			"Map data with a fixed UTL-X script, data in and data out: takes the input inline (a JSON value, or XML/CSV/YAML text) or several named inputs, and returns the result inline (JSON as an object, other formats as text). Built for workflow tool nodes; no artifacts needed.",
			mapData,
			sdk.WithConfigSchema(cfg...), sdk.WithTimeout(120)),
	}
}

// ---------- utlx_transform ----------

type TransformParams struct {
	Script         *string        `json:"script" desc:"UTL-X script. Either a full script starting with '%utlx 1.0' or only the body after '---' (then input_format/output_format build the header). Omit when transformer is given."`
	Transformer    *sdk.Artifact  `json:"transformer" desc:"Filename of a saved .utlx transformer artifact to run instead of script."`
	Input          *sdk.Artifact  `json:"input" desc:"Filename of the input artifact (single input)."`
	InputText      *string        `json:"input_text" desc:"Input data given inline instead of an artifact (small payloads only)."`
	Inputs         []sdk.Artifact `json:"inputs,omitempty" desc:"Several input artifacts for a multi-input script (header 'input: a json, b csv')."`
	InputNames     []string       `json:"input_names,omitempty" desc:"Names for the inputs, in the same order (referenced as $name in the script). Defaults to the file stems."`
	InputFormat    *string        `json:"input_format" desc:"Input format when the script has no header: json, xml, csv, yaml, odata, xsd, jsch, avro, proto, osch, tsch or auto. Default: from the file extension, else auto."`
	OutputFormat   *string        `json:"output_format" desc:"Output format when the script has no header (default json), or to override the header's output format."`
	OutputFilename *string        `json:"output_filename" desc:"Name of the result artifact. Default: input name with the output format's extension."`
}

func transform(ctx context.Context, p TransformParams, tc *sdk.ToolContext) (*sdk.Result, error) {
	bin, err := locateUtlx(tc)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("UTLX_NOT_FOUND")), nil
	}
	script, scriptSource, err := resolveScript(p.Script, p.Transformer)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
	}
	ws, err := newWorkspace()
	if err != nil {
		return nil, err
	}
	defer ws.close()

	inputs, basis, err := writeInputs(ws, p.Input, p.InputText, p.Inputs, p.InputNames, p.InputFormat)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
	}

	outFmt := "json"
	if p.OutputFormat != nil && strings.TrimSpace(*p.OutputFormat) != "" {
		if outFmt, err = normalizeFormat(*p.OutputFormat, false); err != nil {
			return sdk.Error(err.Error(), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
		}
	}
	inFmt := "auto"
	if len(inputs) == 1 {
		inFmt = inputs[0].format
	}
	full := buildScript(script, inputs, len(inputs) > 1, inFmt, outFmt)
	if hasHeader(script) {
		if f := scriptOutputFormat(full); f != "" && (p.OutputFormat == nil || strings.TrimSpace(*p.OutputFormat) == "") {
			outFmt = f
		}
	}

	scriptPath, err := ws.write("transform.utlx", []byte(full))
	if err != nil {
		return nil, err
	}
	outPath := filepath.Join(ws.dir, "result.out")
	args := []string{"transform", scriptPath}
	args = append(args, inputArgs(inputs)...)
	if hasHeader(script) && p.OutputFormat != nil && strings.TrimSpace(*p.OutputFormat) != "" {
		args = append(args, "--to", outFmt)
	}
	args = append(args, "-o", outPath, "--no-capture")

	_ = tc.SendStatus("Running UTL-X transformation (" + scriptSource + ")…")
	res, err := runUtlx(ctx, bin, ws.dir, maxRuntime(tc), args...)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("UTLX_RUN_FAILED")), nil
	}
	if res.exitCode != 0 {
		return sdk.Error("UTL-X transformation failed:\n"+errorText(res), sdk.WithErrorCode("UTLX_TRANSFORM_ERROR"),
			sdk.WithData(map[string]any{"script": full})), nil
	}
	return resultArtifact(outPath, res, outputName(p.OutputFilename, basis, outFmt), outFmt,
		"Transformed with "+scriptSource)
}

// ---------- utlx_convert ----------

type ConvertParams struct {
	Input          *sdk.Artifact `json:"input" desc:"Filename of the artifact to convert."`
	InputText      *string       `json:"input_text" desc:"Data given inline instead of an artifact (small payloads only)."`
	From           *string       `json:"from" desc:"Source format: json, xml, csv, yaml, odata, xsd, jsch, avro, proto, osch, tsch. Default: from the file extension, else auto-detect (data formats only)."`
	To             string        `json:"to" desc:"Target format: json, xml, csv, yaml, odata, xsd, jsch, avro, proto, osch, tsch."`
	OutputFilename *string       `json:"output_filename" desc:"Name of the result artifact. Default: input name with the target format's extension."`
}

func convert(ctx context.Context, p ConvertParams, tc *sdk.ToolContext) (*sdk.Result, error) {
	bin, err := locateUtlx(tc)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("UTLX_NOT_FOUND")), nil
	}
	to, err := normalizeFormat(p.To, false)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
	}
	ws, err := newWorkspace()
	if err != nil {
		return nil, err
	}
	defer ws.close()

	inputs, basis, err := writeInputs(ws, p.Input, p.InputText, nil, nil, p.From)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
	}
	in := inputs[0]
	if k := formats[in.format].kind; k != "" && k != formats[to].kind {
		return sdk.Error(fmt.Sprintf("cannot convert %s (%s) to %s (%s) without a mapping: use utlx_transform with a script instead",
			in.format, formats[in.format].kind, to, formats[to].kind), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
	}
	outPath := filepath.Join(ws.dir, "result.out")
	// Identity mode: utl-x requires a flag (not -i) as the first argument.
	args := []string{"--to", to}
	if in.format != "auto" {
		args = append(args, "--from", in.format)
	}
	args = append(args, "-i", in.path, "-o", outPath, "--no-capture")

	_ = tc.SendStatus(fmt.Sprintf("Converting %s to %s…", in.format, to))
	res, err := runUtlx(ctx, bin, ws.dir, maxRuntime(tc), args...)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("UTLX_RUN_FAILED")), nil
	}
	if res.exitCode != 0 {
		return sdk.Error("UTL-X conversion failed:\n"+errorText(res), sdk.WithErrorCode("UTLX_CONVERT_ERROR")), nil
	}
	return resultArtifact(outPath, res, outputName(p.OutputFilename, basis, to), to,
		fmt.Sprintf("Converted %s to %s", in.format, to))
}

// ---------- utlx_create_transformer ----------

type CreateTransformerParams struct {
	Name            string        `json:"name" desc:"Transformer name, e.g. 'order-to-invoice'. Saved as <name>.utlx."`
	Script          string        `json:"script" desc:"UTL-X script: full (starting with '%utlx 1.0') or only the body after '---' together with input_format/output_format."`
	InputFormat     *string       `json:"input_format" desc:"Input format for a body-only script: json, xml, csv, yaml, odata, xsd, jsch, avro, proto, osch, tsch or auto. Default: from the sample's extension, else auto."`
	OutputFormat    *string       `json:"output_format" desc:"Output format for a body-only script (default json)."`
	Description     *string       `json:"description" desc:"What the transformer does; stored with the artifact."`
	SampleInput     *sdk.Artifact `json:"sample_input" desc:"Filename of a sample input artifact to test-run the transformer on (recommended)."`
	SampleInputText *string       `json:"sample_input_text" desc:"Sample input given inline instead of an artifact."`
}

func createTransformer(ctx context.Context, p CreateTransformerParams, tc *sdk.ToolContext) (*sdk.Result, error) {
	bin, err := locateUtlx(tc)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("UTLX_NOT_FOUND")), nil
	}
	name := strings.TrimSuffix(strings.TrimSpace(p.Name), ".utlx")
	name = strings.Trim(unsafeName.ReplaceAllString(name, "-"), "-.")
	if name == "" {
		return sdk.Error("name is required", sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
	}
	if strings.TrimSpace(p.Script) == "" {
		return sdk.Error("script is required", sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
	}
	ws, err := newWorkspace()
	if err != nil {
		return nil, err
	}
	defer ws.close()

	hasSample := p.SampleInput != nil || (p.SampleInputText != nil && *p.SampleInputText != "")
	var inputs []namedInput
	basis := ""
	if hasSample {
		if inputs, basis, err = writeInputs(ws, p.SampleInput, p.SampleInputText, nil, nil, p.InputFormat); err != nil {
			return sdk.Error(err.Error(), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
		}
	}
	inFmt := "auto"
	if p.InputFormat != nil && strings.TrimSpace(*p.InputFormat) != "" {
		if inFmt, err = normalizeFormat(*p.InputFormat, true); err != nil {
			return sdk.Error(err.Error(), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
		}
	} else if len(inputs) == 1 {
		inFmt = inputs[0].format
	}
	outFmt := "json"
	if p.OutputFormat != nil && strings.TrimSpace(*p.OutputFormat) != "" {
		if outFmt, err = normalizeFormat(*p.OutputFormat, false); err != nil {
			return sdk.Error(err.Error(), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
		}
	}
	full := buildScript(p.Script, nil, false, inFmt, outFmt)
	if f := scriptOutputFormat(full); f != "" {
		outFmt = f
	}
	scriptPath, err := ws.write(name+".utlx", []byte(full))
	if err != nil {
		return nil, err
	}

	_ = tc.SendStatus("Validating transformer " + name + "…")
	val, err := runUtlx(ctx, bin, ws.dir, maxRuntime(tc), "validate", scriptPath)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("UTLX_RUN_FAILED")), nil
	}
	if val.exitCode != 0 {
		return sdk.Error("Transformer not saved: validation failed.\n"+errorText(val), sdk.WithErrorCode("UTLX_VALIDATION_ERROR"),
			sdk.WithData(map[string]any{"script": full})), nil
	}
	data := map[string]any{"transformer": name + ".utlx", "output_format": outFmt, "validated": true}
	if w := strings.TrimSpace(val.stdout + val.stderr); strings.Contains(w, "⚠") {
		// The type checker does not know every stdlib function, so these are
		// hints, not failures; the sample run below is the real test.
		hints, _ := clip(w, 1500)
		data["type_checker_hints"] = "Informational only: the static type checker does not know most stdlib functions " +
			"(map, flatMap, parseNumber, …) and flags them as undefined. Validation passed; the sample run is the real test. " +
			"Do not report these hints to the user as problems.\n" + hints
	}

	if hasSample {
		_ = tc.SendStatus("Test-running " + name + " on the sample…")
		outPath := filepath.Join(ws.dir, "sample.out")
		args := append([]string{"transform", scriptPath}, inputArgs(inputs)...)
		args = append(args, "-o", outPath, "--no-capture")
		run, err := runUtlx(ctx, bin, ws.dir, maxRuntime(tc), args...)
		if err != nil {
			return sdk.Error(err.Error(), sdk.WithErrorCode("UTLX_RUN_FAILED")), nil
		}
		if run.exitCode != 0 {
			return sdk.Error("Transformer not saved: the sample run failed.\n"+errorText(run), sdk.WithErrorCode("UTLX_TRANSFORM_ERROR"),
				sdk.WithData(map[string]any{"script": full})), nil
		}
		out := readOutput(outPath, run)
		prev, cut := clip(out, previewChars)
		data["sample_input"] = basis
		data["sample_output_preview"] = prev
		data["sample_output_truncated"] = cut
	} else {
		data["note"] = "Saved without a test run. Run it on real data with utlx_transform before relying on it."
	}

	desc := "UTL-X transformer " + name
	if p.Description != nil && strings.TrimSpace(*p.Description) != "" {
		desc = strings.TrimSpace(*p.Description)
	}
	return sdk.OK("Transformer "+name+".utlx saved. Run it with utlx_transform(transformer=\""+name+".utlx\", input=...).",
		sdk.WithData(data),
		sdk.WithDataObjects(sdk.DataObject{
			Name:        name + ".utlx",
			Content:     []byte(full),
			MIMEType:    "text/plain",
			Disposition: sdk.DispositionArtifact,
			Description: desc,
		})), nil
}

// ---------- utlx_functions ----------

type FunctionsParams struct {
	Function *string `json:"function" desc:"Exact function name to describe, e.g. 'groupBy'."`
	Query    *string `json:"query" desc:"Keyword to search function names and descriptions, e.g. 'date' or 'xml'."`
	Category *string `json:"category" desc:"Category to list, e.g. String, Array, Date, XML, CSV, JSON, Math, Encoding, Financial."`
}

func functions(ctx context.Context, p FunctionsParams, tc *sdk.ToolContext) (*sdk.Result, error) {
	bin, err := locateUtlx(tc)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("UTLX_NOT_FOUND")), nil
	}
	set := func(s *string) bool { return s != nil && strings.TrimSpace(*s) != "" }
	var args []string
	switch {
	case set(p.Function):
		args = []string{"functions", "info", strings.TrimSpace(*p.Function)}
	case set(p.Query):
		args = []string{"functions", "search", strings.TrimSpace(*p.Query)}
	case set(p.Category):
		args = []string{"functions", "--category", strings.TrimSpace(*p.Category)}
	default:
		args = []string{"functions", "stats"}
	}
	dir, err := os.MkdirTemp("", "utlx-fn-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	res, err := runUtlx(ctx, bin, dir, maxRuntime(tc), args...)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("UTLX_RUN_FAILED")), nil
	}
	out := strings.TrimSpace(res.stdout)
	if res.exitCode != 0 && out == "" {
		return sdk.Error("utl-x functions failed:\n"+errorText(res), sdk.WithErrorCode("UTLX_FUNCTIONS_ERROR")), nil
	}
	text, _ := clip(out, 8000)
	return sdk.OK(text), nil
}

// ---------- shared helpers ----------

// resolveScript returns the script text from either the inline script or a
// saved transformer artifact, plus a short label for status messages.
func resolveScript(script *string, transformer *sdk.Artifact) (string, string, error) {
	hasScript := script != nil && strings.TrimSpace(*script) != ""
	switch {
	case hasScript && transformer != nil:
		return "", "", errors.New("pass either script or transformer, not both")
	case transformer != nil:
		t := transformer.AsText()
		if strings.TrimSpace(t) == "" {
			return "", "", fmt.Errorf("transformer %s is empty", transformer.Filename)
		}
		return t, transformer.Filename, nil
	case hasScript:
		return *script, "inline script", nil
	}
	return "", "", errors.New("script or transformer is required")
}

// writeInputs stores the inputs in the workspace. Exactly one of single
// artifact, inline text or the multi-input list must be given.
func writeInputs(ws *workspace, single *sdk.Artifact, text *string, multi []sdk.Artifact, names []string, format *string) ([]namedInput, string, error) {
	hasText := text != nil && *text != ""
	n := 0
	for _, b := range []bool{single != nil, hasText, len(multi) > 0} {
		if b {
			n++
		}
	}
	if n == 0 {
		return nil, "", errors.New("an input is required: pass input (artifact), input_text, or inputs")
	}
	if n > 1 {
		return nil, "", errors.New("pass only one of input, input_text or inputs")
	}
	forced := ""
	if format != nil && strings.TrimSpace(*format) != "" {
		f, err := normalizeFormat(*format, true)
		if err != nil {
			return nil, "", err
		}
		forced = f
	}
	pick := func(filename string) string {
		if forced != "" {
			return forced
		}
		if f := formatFromFilename(filename); f != "" {
			return f
		}
		return "auto"
	}

	switch {
	case single != nil:
		path, err := ws.write(single.Filename, single.Content)
		if err != nil {
			return nil, "", err
		}
		return []namedInput{{name: inputName(single.Filename), format: pick(single.Filename), path: path}}, single.Filename, nil
	case hasText:
		f := forced
		if f == "" {
			f = "auto"
		}
		ext := ".txt"
		if fi, ok := formats[f]; ok {
			ext = fi.ext
		}
		path, err := ws.write("input"+ext, []byte(*text))
		if err != nil {
			return nil, "", err
		}
		return []namedInput{{name: "input", format: f, path: path}}, "", nil
	}

	if len(names) > 0 && len(names) != len(multi) {
		return nil, "", fmt.Errorf("input_names has %d entries but inputs has %d", len(names), len(multi))
	}
	out := make([]namedInput, 0, len(multi))
	seen := map[string]bool{}
	for i, a := range multi {
		name := inputName(a.Filename)
		if len(names) > 0 {
			name = inputName(names[i])
		}
		if seen[name] {
			return nil, "", fmt.Errorf("duplicate input name %q: set input_names", name)
		}
		seen[name] = true
		path, err := ws.write(a.Filename, a.Content)
		if err != nil {
			return nil, "", err
		}
		out = append(out, namedInput{name: name, format: pick(a.Filename), path: path})
	}
	return out, multi[0].Filename, nil
}

// inputArgs renders the inputs as CLI arguments: a positional file for one
// input, --input name=path for several.
func inputArgs(inputs []namedInput) []string {
	if len(inputs) == 1 {
		return []string{inputs[0].path}
	}
	var args []string
	for _, in := range inputs {
		args = append(args, "--input", in.name+"="+in.path)
	}
	return args
}

func readOutput(path string, r runResult) string {
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		return string(b)
	}
	return r.stdout
}

// resultArtifact stores the output as an artifact and returns a preview.
func resultArtifact(path string, r runResult, name, format, summary string) (*sdk.Result, error) {
	out := readOutput(path, r)
	if strings.TrimSpace(out) == "" {
		return sdk.Error("UTL-X produced no output."+stderrHint(r), sdk.WithErrorCode("UTLX_EMPTY_OUTPUT")), nil
	}
	prev, cut := clip(out, previewChars)
	return sdk.OK(fmt.Sprintf("%s. Result saved as %s (%s, %d bytes).", summary, name, format, len(out)),
		sdk.WithData(map[string]any{
			"artifact":          name,
			"output_format":     format,
			"bytes":             len(out),
			"preview":           prev,
			"preview_truncated": cut,
		}),
		sdk.WithDataObjects(sdk.DataObject{
			Name:        name,
			Content:     []byte(out),
			MIMEType:    mimeFor(format),
			Disposition: sdk.DispositionArtifact,
			Description: summary,
		})), nil
}

func stderrHint(r runResult) string {
	if s := strings.TrimSpace(r.stderr); s != "" {
		t, _ := clip(s, 2000)
		return "\n" + t
	}
	return ""
}

func maxRuntime(tc *sdk.ToolContext) time.Duration {
	secs := 100
	if tc != nil {
		if v, ok := tc.GetConfig("max_runtime_seconds"); ok {
			switch x := v.(type) {
			case float64:
				secs = int(x)
			case int:
				secs = x
			case string:
				if n, err := strconv.Atoi(x); err == nil {
					secs = n
				}
			}
		}
	}
	if secs <= 0 {
		secs = 100
	}
	return time.Duration(secs) * time.Second
}
