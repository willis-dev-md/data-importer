# Parked: MCP server as a source

**Status: parked (not in v1).** Kept here so the thinking is not lost. Revisit when there is a concrete MCP server
to import from. Open questions to answer first: which servers, remote HTTP vs local stdio, authentication, and
whether tools return structured output or text.

Where it would fit if revived: a new adapter kind `mcp` in `internal/source/`, roadmap milestone M6b (after the
HTTP JSON adapter), reusing the same record path, pagination, schedule, source-group templating and mapping
machinery as `http_json`. Tests would use an in-process fake MCP server (structured/text results, `isError`,
cursor pagination, timeouts, oversized results, read-only guard).

## Draft design

The importer acts as an **MCP client** and calls a tool (or reads a resource) on a configured server. There is no
LLM in the loop: the call is deterministic, exactly as configured, and the result is parsed like any API response.

```yaml
kind: mcp
mcp:
  server:
    transport: streamable_http          # streamable_http | stdio
    url: https://mcp.example.com/mcp    # stdio: command + args instead
    auth: { type: bearer, token_env: EXAMPLE_MCP_TOKEN }
  call:
    tool: list_observations             # or `resource: <uri>` for resources/read
    arguments: { series: "{series_id}", from: "2020-01-01" }   # placeholders work with source groups
  result:
    content: json                       # json | text(csv|json) | structured   (which part of the result to parse)
    records_path: $.items[*]
  pagination:
    type: cursor                        # same options as http_json; the cursor is passed as a tool argument
    cursor_path: $.next
    cursor_arg: cursor
  timeout: 60s
natural_key: [id]
```

Notes:
- Results come from `structuredContent` when the tool provides it, otherwise from text content parsed per `content`.
  A result with `isError: true` fails the run with the server's message.
- `importer infer` can call `tools/list` to show a server's tools and input schemas, and draft the `call` block.
- Servers are third-party code or services: stdio servers are an allow-listed command run in a sandbox with a scrubbed
  environment, remote servers must be on the network allowlist, and tool results are treated as **untrusted data**
  (size limits, no execution, never fed to an LLM).
- Tool calls with side effects must never be scheduled. Only tools explicitly marked `read_only: true` in the source
  config are allowed, and the import fails validation otherwise.
