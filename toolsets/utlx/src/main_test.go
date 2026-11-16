package main

// Integration tests: they run the real tools in-process against a utl-x
// binary. Set UTLX_BIN to the binary for your host (e.g. the macOS or Linux
// release from https://github.com/grauwen/utl-x/releases); without it the
// tests are skipped.
//
//   UTLX_BIN=/path/to/utlx go test ./...

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/SolaceDev/solace-agent-mesh-go/pkg/samtoolsdk"
)

const orderXML = `<Order id="ORD-1"><Item sku="A" qty="2" price="9.5"/><Item sku="B" qty="1" price="20"/></Order>`

const personXSD = `<?xml version="1.0"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:element name="Person"><xs:complexType><xs:sequence>
    <xs:element name="name" type="xs:string"/>
    <xs:element name="age" type="xs:int" minOccurs="0"/>
  </xs:sequence></xs:complexType></xs:element>
</xs:schema>`

type harness struct {
	t      *testing.T
	bin    string
	inDir  string
	outDir string
}

func newHarness(t *testing.T) *harness {
	bin := os.Getenv("UTLX_BIN")
	if bin == "" {
		t.Skip("UTLX_BIN not set")
	}
	return &harness{t: t, bin: bin, inDir: t.TempDir(), outDir: t.TempDir()}
}

// file stages an input artifact and returns its path.
func (h *harness) file(name, content string) string {
	p := filepath.Join(h.inDir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return p
}

// call invokes a tool; artifacts maps param keys ("input", "inputs[0]") to staged files.
func (h *harness) call(tool string, args map[string]any, artifacts map[string]string) map[string]any {
	meta := map[string]map[string]any{}
	for k, p := range artifacts {
		meta[k] = map[string]any{"filename": filepath.Base(p), "version": 1}
	}
	res, err := sdk.InvokeInProcess(context.Background(), tools(), tool, args,
		map[string]any{"utlx_path": h.bin},
		sdk.InProcessBindings{OutputDir: h.outDir, ArtifactPaths: artifacts, ArtifactMeta: meta})
	if err != nil {
		h.t.Fatalf("%s: %v", tool, err)
	}
	m, _ := res.(map[string]any)
	return m
}

func (h *harness) output(name string) string {
	b, err := os.ReadFile(filepath.Join(h.outDir, name))
	if err != nil {
		h.t.Fatalf("output artifact %s: %v", name, err)
	}
	return string(b)
}

func mustSucceed(t *testing.T, r map[string]any) {
	t.Helper()
	if r["status"] != "success" {
		t.Fatalf("expected success, got %v: %v", r["status"], r["message"])
	}
}

func TestTransformBodyOnlyXMLToCSV(t *testing.T) {
	h := newHarness(t)
	r := h.call("utlx_transform", map[string]any{
		"script":        `$input.Order.Item |> map(i => {sku: i.@sku, total: parseNumber(i.@qty) * parseNumber(i.@price)})`,
		"input":         "order.xml",
		"output_format": "csv",
	}, map[string]string{"input": h.file("order.xml", orderXML)})
	mustSucceed(t, r)
	out := h.output("order.csv")
	if !strings.Contains(out, "sku") || !strings.Contains(out, "A") || !strings.Contains(out, "19") {
		t.Fatalf("unexpected CSV:\n%s", out)
	}
}

func TestTransformFullScriptInlineInput(t *testing.T) {
	h := newHarness(t)
	r := h.call("utlx_transform", map[string]any{
		"script":          "%utlx 1.0\ninput json\noutput yaml\n---\n{greeting: \"Hello \" + $input.name}\n",
		"input_text":      `{"name":"World"}`,
		"input_format":    "json",
		"output_filename": "hello.yaml",
	}, nil)
	mustSucceed(t, r)
	if out := h.output("hello.yaml"); !strings.Contains(out, "Hello World") {
		t.Fatalf("unexpected YAML:\n%s", out)
	}
}

func TestTransformMultiInput(t *testing.T) {
	h := newHarness(t)
	r := h.call("utlx_transform", map[string]any{
		"script": `$orders |> map(o => {id: o.id, customer: ($customers |> filter(c => toString(c.id) == toString(o.c)) |> first()).name})`,
		"inputs": []any{"orders.json", "customers.csv"},
	}, map[string]string{
		"inputs[0]": h.file("orders.json", `[{"id":1,"c":7}]`),
		"inputs[1]": h.file("customers.csv", "id,name\n7,Ann\n"),
	})
	mustSucceed(t, r)
	if out := h.output("orders.json"); !strings.Contains(out, "Ann") {
		t.Fatalf("unexpected join result:\n%s", out)
	}
}

func TestTransformErrorIsReported(t *testing.T) {
	h := newHarness(t)
	r := h.call("utlx_transform", map[string]any{
		"script":     "{a: $input.x |> map(",
		"input_text": `{"x":[1]}`,
	}, nil)
	if r["status"] != "error" || !strings.Contains(r["message"].(string), "failed") {
		t.Fatalf("expected a reported error, got %v", r)
	}
}

func TestConvertDataAndSchemas(t *testing.T) {
	h := newHarness(t)
	order := h.file("order.xml", orderXML)
	mustSucceed(t, h.call("utlx_convert", map[string]any{"input": "order.xml", "to": "yaml"}, map[string]string{"input": order}))
	if out := h.output("order.yaml"); !strings.Contains(out, "ORD-1") {
		t.Fatalf("unexpected YAML:\n%s", out)
	}

	xsd := h.file("person.xsd", personXSD)
	for to, want := range map[string]string{"jsch": "json-schema.org", "avro": `"record"`, "proto": "proto3"} {
		r := h.call("utlx_convert", map[string]any{"input": "person.xsd", "to": to}, map[string]string{"input": xsd})
		mustSucceed(t, r)
		name := r["data"].(map[string]any)["artifact"].(string)
		if out := h.output(name); !strings.Contains(out, want) {
			t.Fatalf("xsd->%s: missing %q in\n%s", to, want, out)
		}
	}

	r := h.call("utlx_convert", map[string]any{"input": "person.xsd", "to": "json"}, map[string]string{"input": xsd})
	if r["status"] != "error" {
		t.Fatalf("schema->data conversion should be refused, got %v", r)
	}
}

func TestCreateTransformerThenRun(t *testing.T) {
	h := newHarness(t)
	order := h.file("order.xml", orderXML)
	r := h.call("utlx_create_transformer", map[string]any{
		"name":          "order-to-lines",
		"script":        `$input.Order.Item |> map(i => {sku: i.@sku, qty: parseNumber(i.@qty)})`,
		"output_format": "json",
		"sample_input":  "order.xml",
	}, map[string]string{"sample_input": order})
	mustSucceed(t, r)
	script := h.output("order-to-lines.utlx")
	if !strings.HasPrefix(script, "%utlx 1.0\ninput xml\noutput json") {
		t.Fatalf("unexpected saved transformer:\n%s", script)
	}

	saved := h.file("order-to-lines.utlx", script)
	r = h.call("utlx_transform", map[string]any{"transformer": "order-to-lines.utlx", "input": "order.xml"},
		map[string]string{"transformer": saved, "input": order})
	mustSucceed(t, r)
	if out := h.output("order.json"); !strings.Contains(out, `"qty": 2`) && !strings.Contains(out, `"qty":2`) {
		t.Fatalf("unexpected output:\n%s", out)
	}

	bad := h.call("utlx_create_transformer", map[string]any{"name": "broken", "script": "{a: (", "output_format": "json"}, nil)
	if bad["status"] != "error" {
		t.Fatalf("invalid script should not be saved, got %v", bad)
	}
	if _, err := os.Stat(filepath.Join(h.outDir, "broken.utlx")); err == nil {
		t.Fatal("broken.utlx was saved")
	}
}

func TestFunctionsLookup(t *testing.T) {
	h := newHarness(t)
	r := h.call("utlx_functions", map[string]any{"function": "groupBy"}, nil)
	mustSucceed(t, r)
	if !strings.Contains(r["message"].(string), "groupBy") {
		t.Fatalf("unexpected info: %v", r["message"])
	}
	r = h.call("utlx_functions", map[string]any{"query": "date"}, nil)
	mustSucceed(t, r)
}

func TestMapJSONObjectToJSON(t *testing.T) {
	h := newHarness(t)
	r := h.call("utlx_map", map[string]any{
		"script": `{customer: upper($input.name), total: sum($input.items |> map(i => i.qty * i.price))}`,
		"data":   map[string]any{"name": "ann", "items": []any{map[string]any{"qty": 2, "price": 9.5}, map[string]any{"qty": 1, "price": 20}}},
	}, nil)
	mustSucceed(t, r)
	out := r["data"].(map[string]any)["output"].(map[string]any)
	if out["customer"] != "ANN" || out["total"].(float64) != 39 {
		t.Fatalf("unexpected output: %v", out)
	}
}

func TestMapXMLTextToCSV(t *testing.T) {
	h := newHarness(t)
	r := h.call("utlx_map", map[string]any{
		"script":        `$input.Order.Item |> map(i => {sku: i.@sku, qty: parseNumber(i.@qty)})`,
		"data":          orderXML,
		"input_format":  "xml",
		"output_format": "csv",
	}, nil)
	mustSucceed(t, r)
	out := r["data"].(map[string]any)["output"].(string)
	if !strings.Contains(out, "sku") || !strings.Contains(out, "A,2") {
		t.Fatalf("unexpected CSV: %q", out)
	}
}

func TestMapNamedInputs(t *testing.T) {
	h := newHarness(t)
	r := h.call("utlx_map", map[string]any{
		"script": `$orders |> map(o => {id: o.id, customer: ($customers |> filter(c => toString(c.id) == toString(o.c)) |> first()).name})`,
		"inputs": map[string]any{
			"orders":    []any{map[string]any{"id": 1, "c": 7}},
			"customers": "id,name\n7,Ann\n",
		},
		"input_formats":    map[string]any{"customers": "csv"},
		"save_as_artifact": "joined.json",
	}, nil)
	mustSucceed(t, r)
	arr := r["data"].(map[string]any)["output"].([]any)
	if arr[0].(map[string]any)["customer"] != "Ann" {
		t.Fatalf("unexpected join: %v", arr)
	}
	if !strings.Contains(h.output("joined.json"), "Ann") {
		t.Fatal("artifact not saved")
	}
	// A single named input must still be reachable as $name.
	r = h.call("utlx_map", map[string]any{
		"script": `{n: count($orders)}`,
		"inputs": map[string]any{"orders": []any{1, 2, 3}},
	}, nil)
	mustSucceed(t, r)
	if r["data"].(map[string]any)["output"].(map[string]any)["n"].(float64) != 3 {
		t.Fatalf("unexpected: %v", r["data"])
	}
}

func TestMapErrors(t *testing.T) {
	h := newHarness(t)
	if r := h.call("utlx_map", map[string]any{"script": "{a: 1}"}, nil); r["status"] != "error" {
		t.Fatalf("missing data should fail: %v", r)
	}
	if r := h.call("utlx_map", map[string]any{"script": "{a: (", "data": map[string]any{}}, nil); r["status"] != "error" {
		t.Fatalf("bad script should fail: %v", r)
	}
}
