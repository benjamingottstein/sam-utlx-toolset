# UTL-X toolset for Solace Agent Mesh

A Go toolset for [Solace Agent Mesh](https://solace.com/products/agent-mesh/) (SAM) that lets agents create and run [UTL-X](https://github.com/grauwen/utl-x) transformers. It supports every format utl-x does:

| Kind | Formats (`--from` / `--to` value) |
|---|---|
| Data | JSON (`json`), XML (`xml`), CSV (`csv`), YAML (`yaml`), OData (`odata`) |
| Schemas | XSD (`xsd`), JSON Schema (`jsch`), Avro (`avro`), Protobuf (`proto`), OData/EDMX (`osch`), Table Schema (`tsch`) |

## Tools

| Tool | What it does |
|---|---|
| `utlx_transform` | Runs a UTL-X script (inline, or a saved `.utlx` transformer artifact) on one input artifact, inline text, or several named inputs (joins). Saves the result as an artifact and returns a preview. |
| `utlx_convert` | Converts a file between formats without a script, e.g. XML → YAML, CSV → JSON, XSD → JSON Schema / Avro / Protobuf. |
| `utlx_create_transformer` | Validates a script, test-runs it on a sample input and saves it as `<name>.utlx`. Nothing is saved if validation or the sample run fails, and the errors come back to the agent so it can fix them. |
| `utlx_functions` | Looks up the stdlib (~700 functions): one function's signature and examples, a keyword search, or a category. |

A script can be passed as a full UTL-X script (`%utlx 1.0` header) or as just the body. In the second case the tool builds the header from `input_format` / `output_format`. The toolset also injects a short UTL-X cheat sheet into the agent's system prompt.

An example agent, [`agents/data-transformer.yaml`](agents/data-transformer.yaml), uses all four tools.

```text
You: Turn orders.xml into a CSV with one row per order line (order id, sku, qty, line total), ';' as delimiter,
     and save it as a reusable transformer named orders-to-lines.
Agent: utlx_convert (inspect) → utlx_transform → utlx_create_transformer
       → orders-lines.csv + orders-to-lines.utlx
```

## How it works

```text
agent ──► Secure Tool Runtime ──► utlx (Go tool binary, samtoolsdk)
                                   └─► bin/utlx  (utl-x native CLI, bundled in the zip)
```

The Go binary validates arguments, writes the input artifacts to a scratch directory, runs the utl-x CLI with a timeout, and returns the output as an artifact. utl-x runs fully offline, so the tools need no network access.

## Requirements

- SAM CLI (`sam`) 2.542 or later, and a SAM platform or the SAM desktop app
- Go 1.26 or later
- A utl-x binary for the Secure Tool Runtime's OS/arch. `build.sh` downloads it from the utl-x releases:

| STR target | utl-x binary | Notes |
|---|---|---|
| `linux/amd64` | `utlx-linux-x64` (release) | Needs a glibc-based STR image (the binary is dynamically linked) |
| `darwin/arm64` | `utlx-macos-arm64` (release) | SAM desktop app on Apple Silicon |
| `linux/arm64` | none published | Build it with [`toolsets/utlx/tools/build-utlx-linux-arm64.sh`](toolsets/utlx/tools/build-utlx-linux-arm64.sh) (GraalVM in a container) and pass `UTLX_BINARY` |

Find out what your platform expects:

```bash
sam toolset build-target --url https://<your-sam-platform>
```

## Quick start

```bash
git clone https://github.com/benjamingottstein/sam-utlx-toolset
```

```bash
cd sam-utlx-toolset && sam toolset sync
```

`sam toolset sync` vendors the SAM tool SDK into `toolsets/utlx/src/_sdk/`. It ships inside the `sam` CLI and is not committed here.

### Test locally

The integration tests run the tools in-process against a real utl-x binary for your machine:

```bash
cd toolsets/utlx/src && UTLX_BIN=/path/to/utlx go test ./...
```

Check that the Secure Tool Runtime can discover the tools:

```bash
sam toolset validate utlx
```

### Try it in the SAM desktop app

```bash
sam config apply -m manifests/desktop.yaml
```

```bash
sam task send "Convert the attached file to YAML" -a data-transformer -f ./some.xml
```

> Never add `--prune` to these commands. It deletes every resource on the target that this manifest does not list.

### Deploy to a SAM platform

Package a bundle for your Secure Tool Runtime and upload it on the **Toolsets** page:

```bash
sam toolset package utlx --url https://<your-sam-platform>
```

For linux/arm64, build utl-x first, then package with your binary:

```bash
./toolsets/utlx/tools/build-utlx-linux-arm64.sh
```

```bash
UTLX_BINARY=$PWD/toolsets/utlx/tools/out/utlx-linux-arm64 SAM_TOOL_TARGET_OS=linux SAM_TOOL_TARGET_ARCH=arm64 sam toolset package utlx
```

Or apply declaratively. Copy `manifests/desktop.yaml`, set `target.url` and `target.auth`, and run `sam config apply -m <your manifest>`. Then attach the `utlx` toolset to any agent, or use `agents/data-transformer.yaml`.

The bundle is about 35 MB zipped and 100 MB unpacked, within SAM's upload limits (50 MB zipped, 250 MB unpacked).

## Configuration

Per-agent tool configuration, all optional:

| Key | Default | Meaning |
|---|---|---|
| `utlx_path` | bundled `bin/utlx` | Path to a utl-x binary, e.g. one baked into a custom STR image (then build with `UTLX_SKIP_BINARY=1`) |
| `max_runtime_seconds` | `100` | Kills a utl-x run after this many seconds |

Build-time variables for `build.sh`: `UTLX_VERSION` (default `1.3.1`), `UTLX_BINARY`, `UTLX_SHA256`, `UTLX_SKIP_BINARY`.

To limit who can run a tool, use the toolset's `required_scopes` config (see the SAM docs on toolsets).

## Known quirks (utl-x 1.3.1)

- **Type-checker warnings:** `utlx validate` warns that many stdlib functions (`map`, `parseNumber`, …) are undefined variables. The tools report these as informational hints only. A script that runs is valid.
- **Comparing keys across inputs:** numbers coming from different inputs or formats may not compare equal. Normalise both sides first, e.g. `toString(a.id) == toString(b.id)`. The agent instructions include this rule.
- **XML attribute values:** these are strings. Use `parseNumber` before doing arithmetic on them.

## Licensing

This repository contains only the SAM toolset code. The SAM tool SDK is not included; `sam toolset sync` provides it from your licensed SAM CLI.

utl-x is licensed under [AGPL-3.0](https://github.com/grauwen/utl-x/blob/main/LICENSE.md). `build.sh` downloads the unmodified utl-x release binary at build time and bundles it in the toolset zip as a separate executable, which the tools call as a subprocess. If you distribute that zip, the AGPL obligations for utl-x apply to the bundled binary (provide its license and point to its source). Check this with your own legal team.
