# DeepSeek text input admission

Policy `text-json-utf8-framing.v2` bounds the **complete** request, including all
message contents, historical arguments/results and the fixed tool schemas. It
changes no context, tool return, citation or evidence digest. It does not truncate,
summarize, deduplicate or discard repeated reads. Token/request/output/time caps
and retry behavior are unchanged.

## Supported renderer and tokenizer

The proof assumes the published DeepSeek V4 non-thinking (`chat`) encoder at
revision `0e1a0e5e52aea73055f50fef6f2423db370265b6`:

- [encoding_dsv4.py](https://huggingface.co/deepseek-ai/DeepSeek-V4-Pro/blob/0e1a0e5e52aea73055f50fef6f2423db370265b6/encoding/encoding_dsv4.py),
  SHA256 `bdbd57c132a1b3725042323d02b98b9d1df28e5f388f134399555d041f5055e0`.
- [tokenizer.json](https://huggingface.co/deepseek-ai/DeepSeek-V4-Pro/blob/0e1a0e5e52aea73055f50fef6f2423db370265b6/tokenizer.json),
  SHA256 `8f9f37ca37fdc4f5fd36d5cf4d3b0e8392edb4e894fd10cc0d70b4957c8633cf`.

The tokenizer has no normalization expansion or BPE continuation/end suffix.
Its pre-tokenizer splits text and then applies ByteLevel without adding a prefix
space. Each UTF-8 byte initially has at most one unit; BPE merges only decrease
that count. A special token consumes at most its nonempty literal byte length.
Thus rendered UTF-8 bytes bound input tokens. This does not require a tokenizer
dependency at runtime.

The accepted request uses `deepseek-v4-pro`, disabled thinking, required tool
choice and non-streaming output. Messages have only system/user/assistant/tool
roles, text or null content, optional function calls and tool-call IDs. Each
assistant message has at most four calls, with at most three JSON object members
per call's arguments. Function schemas must equal the harness's frozen schemas
(an empty tool list is also supported). Additional fields, reasoning history,
arbitrary schemas, multimodal content and other renderer branches refuse before
reservation. These restrictions make the proof's supported surface executable.

## Byte envelope

Let `B` be the byte length of `json.dumps(payload, ensure_ascii=True,
separators=(",", ":"), allow_nan=False)`, `M` the original message count, `C` the
historical function call count and `A` the sum of their argument member counts.
The token upper bound is:

```text
2B + 4096 + 128M + 128C + 128A
```

`2B` covers all variable material. The renderer copies message text once,
serializes each function schema once, and copies each historical name, argument
key and string value once. Non-string argument values are JSON serialized once.
Their variable material occupies disjoint regions of the complete payload; the
JSON wrapper and call IDs are additionally counted even where rendering omits
them. Decoding JSON escapes cannot increase UTF-8 bytes beyond their ASCII-escaped
representation. Default JSON spaces add at most one byte for each comma/colon
already counted in the compact representation. Twice the complete representation
therefore covers these serializations, including nested objects, Unicode, control
characters, escaped JSON within result strings and repeated results. It also
retains a substantial variable-material margin; no observed usage ratio is used.

The fixed additions follow the pinned source's actual rendering branches:

| Construct | Pinned rendered bytes, excluding variable material | Allocation |
| --- | --- | --- |
| Global tools instructions and preceding separator | 991 | 4096 once |
| BOS and up to two schema joining newlines | less than 64 | included globally |
| User/assistant transition, non-thinking suffix and EOS | at most 64 per original message | 128 per message |
| Assistant tool-call block delimiters and preceding separators | 49 | included in that message's 128 |
| Tool-result wrapper and block separator | 29 | included in that message's 128 |
| Function invoke wrapper and joining newline | 48 per call | 128 per call |
| Parameter wrapper (`false` is longest) and joining newline | 67 per argument | 128 per argument |

Assistant call-block framing plus transitions fits in 113 bytes. A tool result
plus transitions fits in 93. Merging adjacent user/tool messages can only reduce
message framing; sorting tool results neither duplicates nor drops them. Global
instructions and schemas are attached once to the system message. Response-format,
reasoning-effort, task and other optional branches are outside the accepted subset.
The global 4096 allowance retains unused margin over the measured fixed prefix.

The old policy allocated that entire 4096 allowance again for every message and
schema, despite allocating variable content twice already. Version 2 charges
each fixed construct explicitly. It does not merely rescale a token estimate.

The remote API's deployed encoder/tokenizer and absence of extra server context
remain provider assumptions, not remotely attested facts. An actual prompt usage
above this bound is retained and halts the batch. Missing usage and uncertain
outcomes also halt. Source-derived admission cannot establish an unconditional
provider-enforced spending limit. Review these assumptions before changing the
model, renderer, tools or profile; new semantics require a new policy identity.

## Identity, existing batches and continuation

The batch policy freezes the encoder revision/hashes, schema digest, formula
version and each coefficient alongside the existing caps. The SDK identity
includes the batch ID and entire policy; workflow preflight compares that frozen
identity before dispatch, decision journaling or graph progression. Old batch
policies require the explicit audited compatibility operation described in
[acceptance continuation](CONTINUATION.md). Old SDK workflow identities refuse
to resume. The compatibility operation preserves the original policy and every
request in an audit snapshot, adopts v2 for a linked fresh workflow in the same
cumulative batch and requires explicit approval and independent check references.

An expired or externally stopped batch requires explicit continuation authority
in addition to numeric allowance. Offline replay uses separate fixture ledgers.
The one continuation window starts with its first durable reservation, retains
all historical usage and refusals, and preserves the original expired window.
Unknown outcomes, pending requests and other stops require reconciliation. The
input formula and provider assumptions above apply to both fresh v2 batches and
explicitly continued batches. A policy update grants no additional allowance.
