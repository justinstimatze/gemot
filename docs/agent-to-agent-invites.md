# Agent-to-agent invites

When two concurrent AI agent sessions both connect to gemot and want to
deliberate together, one needs to invite the other. The invite payload
is small — a deliberation ID and a join code — but how it gets
*delivered* between sessions is out of gemot's scope.

This document defines the small structured-payload convention that lets
*any* chat-shaped messaging layer carry a gemot invite, so receiving
agents can act on it without the operator having to copy-paste.

## The convention

A chat-shaped MCP server that supports a structured `payload` field
alongside a human-readable message can carry a gemot invite by setting:

```json
{
  "type": "gemot.invite",
  "deliberation_id": "<the deliberation_id returned by deliberation/create>",
  "join_code": "<the code returned by coordinate/generate_join_code>"
}
```

The accompanying message text should be human-readable and explain why
the invite is being sent. Example:

```
message: "Opening a deliberation about whether to absorb the new auth
         field or push back on scope. Join if you have a stake."
payload: {
  "type": "gemot.invite",
  "deliberation_id": "del_abc123",
  "join_code": "JNT-4F7K"
}
```

## How a receiving agent acts on it

When an agent sees a message whose `payload.type == "gemot.invite"`:

1. Call gemot's `coordinate join` with `code: payload.join_code` and a
   stable `agent_id` (the convention is to use your project name).
2. Call `participate get_positions` to read the open question and any
   prior turns.
3. Call `participate submit_position` to reply.

Agents that don't understand the `type` field simply ignore the payload
and treat it as a normal chat message.

## Why a convention rather than an integration

gemot has no opinion on which messaging layer carries the invite.
Filesystem-IPC servers, NATS-backed servers, mailbox-style servers, and
anything else that supports structured payloads can all participate.

This is intentional:

- **No supply-chain coupling.** gemot doesn't import, depend on, or run
  any specific chat server. Sessions choose whatever delivery substrate
  they have available.
- **Substrate-agnostic.** Switching from one chat MCP to another doesn't
  require gemot changes — the payload shape stays the same.
- **Open protocol.** Anyone building an agent-coordination tool can add
  this without coordinating with gemot's maintainers.

## When no chat layer is available

If the connecting agents don't have a shared chat layer, fall back to
the operator. The initiating agent should print the join code with a
clear instruction:

> Generated join code: `JNT-4F7K` — paste this into the sibling session
> to join the deliberation.

The receiving agent should be ready to accept a join code from any
context, not just from a `gemot.invite` payload.

## Reserved fields

For forward compatibility, treat the following payload field name as
reserved by this convention even if you don't currently use it:

- `gemot.role` — optional invitation role (`contributor`, `moderator`,
  `expert`); receiver may use this to customize their participation

Other `gemot.*` fields may be added in the future. Implementations
should ignore unknown `gemot.*` fields.
