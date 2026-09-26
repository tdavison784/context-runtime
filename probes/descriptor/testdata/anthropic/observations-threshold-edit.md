# Anthropic descriptor probe: raw observations

Generated 2026-09-26T13:04:43Z by probes/descriptor/anthropic. Estimated spend $0.0056.

| probe | model | status | stop | in | out | cache w | cache r | blocks / transformations / error / note |
|---|---|---|---|---|---|---|---|---|
| compaction/claude-opus-5-5/T4a-threshold-block-without-strategy | claude-opus-5-5 | 400 |  | 0 | 0 | 0 | 0 |  error=invalid_request_error: messages.1.content.0: `compaction` blocks require a `compact_20260112` strategy in `context_management.edits`. |
| compaction/claude-opus-5-5/T4-threshold-block-edited | claude-opus-5-5 | 200 | end_turn | 888 | 104 | 0 | 0 | thinking(text=166 sig=1116) text("Sixty-two") transforms=<absent> iterations=[{"input_tokens":888,"output_tokens":104,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message"}] |
