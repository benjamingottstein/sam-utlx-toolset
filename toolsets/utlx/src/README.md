# utlx toolset sources

Go sources of the `utlx` toolset. See the [repository README](../../../README.md) for what the tools do and how to build, test and deploy them.

| File | Purpose |
|---|---|
| `main.go` | Tool definitions (`utlx_transform`, `utlx_convert`, `utlx_create_transformer`, `utlx_functions`) and the agent instructions |
| `utlx.go` | Helpers: locate and run the utl-x CLI, formats, script headers |
| `main_test.go` | Integration tests (need `UTLX_BIN`) |
| `build.sh` / `build.bat` | Build the tool binary and bundle the utl-x CLI as `bin/utlx` |
| `manifest.yaml` | Secure Tool Runtime registration |
| `_sdk/` | Vendored `samtoolsdk` (not in Git; created by `sam toolset sync`) |
