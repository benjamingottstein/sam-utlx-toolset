package main

// utlx_map: a data-in, data-out mapping tool for workflow tool nodes. The
// script is fixed in the node definition, the data comes from the workflow
// (inline, no artifacts), and the result is returned inline so downstream
// nodes and the workflow's output_mapping can use it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	sdk "github.com/SolaceDev/solace-agent-mesh-go/pkg/samtoolsdk"
)

// payload accepts any JSON value. A JSON string is taken as raw text (XML,
// CSV, YAML or JSON text); an object, array, number or boolean is kept as its
// JSON encoding. Its kind is string, so the advertised schema stays a plain
// string that every LLM provider accepts, while workflows may pass objects.
type payload string

func (p *payload) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*p = payload(s)
		return nil
	}
	*p = payload(b)
	return nil
}

type MapParams struct {
	Script         string             `json:"script" desc:"UTL-X script: full (starting with '%utlx 1.0') or only the body after '---' (then input_format/output_format build the header)."`
	Data           *payload           `json:"data" desc:"The input: a JSON value (object or array) or text (XML, CSV, YAML, JSON). Use inputs instead for several named inputs."`
	Inputs         map[string]payload `json:"inputs,omitempty" desc:"Named inputs for a multi-input script, e.g. {\"orders\": [...], \"customers\": \"id,name\\n7,Ann\"}; referenced as $orders, $customers."`
	InputFormat    *string            `json:"input_format" desc:"Format of data (or of every input): json, xml, csv, yaml, odata, xsd, jsch, avro, proto, osch, tsch or auto (default: json for a JSON value, else auto)."`
	InputFormats   map[string]string  `json:"input_formats,omitempty" desc:"Per-input formats for inputs, e.g. {\"customers\": \"csv\"}."`
	OutputFormat   *string            `json:"output_format" desc:"Output format for a body-only script (default json)."`
	SaveAsArtifact *string            `json:"save_as_artifact" desc:"Optional filename: also store the result as an artifact."`
}

func mapData(ctx context.Context, p MapParams, tc *sdk.ToolContext) (*sdk.Result, error) {
	bin, err := locateUtlx(tc)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("UTLX_NOT_FOUND")), nil
	}
	if strings.TrimSpace(p.Script) == "" {
		return sdk.Error("script is required", sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
	}
	if (p.Data == nil) == (len(p.Inputs) == 0) {
		return sdk.Error("pass exactly one of data or inputs", sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
	}
	forced := ""
	if p.InputFormat != nil && strings.TrimSpace(*p.InputFormat) != "" {
		if forced, err = normalizeFormat(*p.InputFormat, true); err != nil {
			return sdk.Error(err.Error(), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
		}
	}
	outFmt := "json"
	if p.OutputFormat != nil && strings.TrimSpace(*p.OutputFormat) != "" {
		if outFmt, err = normalizeFormat(*p.OutputFormat, false); err != nil {
			return sdk.Error(err.Error(), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
		}
	}

	ws, err := newWorkspace()
	if err != nil {
		return nil, err
	}
	defer ws.close()

	// guess picks a format for one payload: explicit > JSON value > auto.
	guess := func(v payload, explicit string) (string, error) {
		if explicit != "" {
			return normalizeFormat(explicit, true)
		}
		if forced != "" {
			return forced, nil
		}
		t := strings.TrimSpace(string(v))
		if json.Valid([]byte(t)) && (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) {
			return "json", nil
		}
		return "auto", nil
	}
	write := func(name string, v payload, f string) (string, error) {
		ext := ".txt"
		if fi, ok := formats[f]; ok {
			ext = fi.ext
		}
		return ws.write(name+ext, []byte(v))
	}

	var inputs []namedInput
	if p.Data != nil {
		f, err := guess(*p.Data, "")
		if err != nil {
			return sdk.Error(err.Error(), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
		}
		path, err := write("input", *p.Data, f)
		if err != nil {
			return nil, err
		}
		inputs = []namedInput{{name: "input", format: f, path: path}}
	} else {
		names := make([]string, 0, len(p.Inputs))
		for n := range p.Inputs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			id := inputName(n)
			if id != n {
				return sdk.Error(fmt.Sprintf("input name %q is not a valid identifier (use letters, digits, _)", n), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
			}
			f, err := guess(p.Inputs[n], p.InputFormats[n])
			if err != nil {
				return sdk.Error(err.Error(), sdk.WithErrorCode("INVALID_ARGUMENTS")), nil
			}
			path, err := write(n, p.Inputs[n], f)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, namedInput{name: n, format: f, path: path})
		}
	}

	full := buildScript(p.Script, inputs, p.Data == nil, inputs[0].format, outFmt)
	if f := scriptOutputFormat(full); f != "" {
		outFmt = f
	}
	scriptPath, err := ws.write("map.utlx", []byte(full))
	if err != nil {
		return nil, err
	}
	outPath := filepath.Join(ws.dir, "result.out")
	args := []string{"transform", scriptPath}
	if p.Data != nil {
		args = append(args, inputs[0].path)
	} else {
		for _, in := range inputs {
			args = append(args, "--input", in.name+"="+in.path)
		}
	}
	args = append(args, "-o", outPath, "--no-capture")

	res, err := runUtlx(ctx, bin, ws.dir, maxRuntime(tc), args...)
	if err != nil {
		return sdk.Error(err.Error(), sdk.WithErrorCode("UTLX_RUN_FAILED")), nil
	}
	if res.exitCode != 0 {
		return sdk.Error("UTL-X mapping failed:\n"+errorText(res), sdk.WithErrorCode("UTLX_TRANSFORM_ERROR")), nil
	}
	out := readOutput(outPath, res)

	data := map[string]any{"output_format": outFmt}
	if outFmt == "json" || outFmt == "jsch" || outFmt == "avro" || outFmt == "tsch" || outFmt == "odata" {
		var v any
		if err := json.Unmarshal([]byte(out), &v); err == nil {
			data["output"] = v
		} else {
			data["output"] = out
		}
	} else {
		data["output"] = out
	}

	opts := []sdk.ResultOption{sdk.WithData(data)}
	msg := fmt.Sprintf("Mapped to %s (%d bytes).", outFmt, len(out))
	if p.SaveAsArtifact != nil && strings.TrimSpace(*p.SaveAsArtifact) != "" {
		name := filepath.Base(strings.TrimSpace(*p.SaveAsArtifact))
		data["artifact"] = name
		msg += " Saved as " + name + "."
		opts = append(opts, sdk.WithDataObjects(sdk.DataObject{
			Name: name, Content: []byte(out), MIMEType: mimeFor(outFmt),
			Disposition: sdk.DispositionArtifact, Description: "UTL-X mapping result",
		}))
	}
	return sdk.OK(msg, opts...), nil
}
