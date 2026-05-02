# Pi gRPC command protocol

ActRail exposes Pi runtime slash commands over HTTP while the Pi process owns command semantics over gRPC.

## HTTP surface

List commands:

```http
GET /api/sessions/{session_id}/commands
```

Response:

```json
{
  "commands": [
    {
      "name": "review",
      "description": "Review current diff",
      "source": "prompt",
      "source_info": {
        "path": "/repo/.pi/agent/prompts/review.md",
        "source": "project",
        "scope": "project",
        "origin": "top-level",
        "base_dir": "/repo"
      }
    }
  ]
}
```

Execute one command:

```http
POST /api/sessions/{session_id}/commands
Content-Type: application/json

{"name":"review","args":"current diff"}
```

`command` is accepted as a request alias for `name`. The leading slash is optional. Response:

```json
{
  "ok": true,
  "command": "review",
  "message": "executed by runtime",
  "session_id": "s_1"
}
```

## Runtime mapping

For Pi sessions launched with gRPC IPC, ActRail calls:

```proto
rpc ListCommands(ListCommandsRequest) returns (ListCommandsResponse);
rpc ExecuteCommand(ExecuteCommandRequest) returns (CommandAck);
```

```proto
message ExecuteCommandRequest {
  string name = 1;
  string args = 2;
}

message ListCommandsResponse {
  repeated SlashCommand commands = 1;
}

message SlashCommand {
  string name = 1;
  string description = 2;
  string source = 3;
  SourceInfo source_info = 4;
}
```

Pi `ExecuteCommand` is equivalent to sending `/<name> <args>` through `Prompt`. Extension commands execute immediately. Prompt templates and skills expand into normal agent turns. The RPC returns after prompt preflight accepts or rejects the command. Generation continues asynchronously through the existing session event stream.

ActRail-owned commands keep local handling before runtime dispatch: `rename`, `focus`, `restart`, `handoff`, and `model`.

For old non-gRPC Pi sessions, unknown commands continue through the existing text prompt path as `/<name> <args>`.
