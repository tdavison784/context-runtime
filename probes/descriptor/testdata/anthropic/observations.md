# Anthropic descriptor probe: raw observations

Generated 2026-09-26T13:03:43Z by probes/descriptor/anthropic. Estimated spend $0.4842.

| probe | model | status | stop | in | out | cache w | cache r | blocks / transformations / error / note |
|---|---|---|---|---|---|---|---|---|
| models/claude-opus-5-5 | claude-opus-5-5 | 200 |  | 0 | 0 | 0 | 0 |  note=Claude Opus 5.5 created=2026-09-21 max_input=1000000 max_output=128000 |
| models/claude-fable-5-1 | claude-fable-5-1 | 200 |  | 0 | 0 | 0 | 0 |  note=Claude Fable 5.1 created=2026-08-28 max_input=1000000 max_output=128000 |
| models/claude-opus-5 | claude-opus-5 | 200 |  | 0 | 0 | 0 | 0 |  note=Claude Opus 5 created=2026-07-24 max_input=1000000 max_output=128000 |
| models/claude-sonnet-5 | claude-sonnet-5 | 200 |  | 0 | 0 | 0 | 0 |  note=Claude Sonnet 5 created=2026-06-29 max_input=1000000 max_output=128000 |
| models/claude-fable-5 | claude-fable-5 | 200 |  | 0 | 0 | 0 | 0 |  note=Claude Fable 5 created=2026-06-07 max_input=1000000 max_output=128000 |
| models/claude-opus-4-8 | claude-opus-4-8 | 200 |  | 0 | 0 | 0 | 0 |  note=Claude Opus 4.8 created=2026-05-28 max_input=1000000 max_output=128000 |
| models/claude-opus-4-7 | claude-opus-4-7 | 200 |  | 0 | 0 | 0 | 0 |  note=Claude Opus 4.7 created=2026-04-14 max_input=1000000 max_output=128000 |
| models/claude-sonnet-4-6 | claude-sonnet-4-6 | 200 |  | 0 | 0 | 0 | 0 |  note=Claude Sonnet 4.6 created=2026-02-17 max_input=1000000 max_output=128000 |
| models/claude-opus-4-6 | claude-opus-4-6 | 200 |  | 0 | 0 | 0 | 0 |  note=Claude Opus 4.6 created=2026-02-04 max_input=1000000 max_output=128000 |
| models/claude-opus-4-5-20251101 | claude-opus-4-5-20251101 | 200 |  | 0 | 0 | 0 | 0 |  note=Claude Opus 4.5 created=2025-11-24 max_input=200000 max_output=64000 |
| models/claude-haiku-4-5-20251001 | claude-haiku-4-5-20251001 | 200 |  | 0 | 0 | 0 | 0 |  note=Claude Haiku 4.5 created=2025-10-15 max_input=200000 max_output=64000 |
| models/claude-sonnet-4-5-20250929 | claude-sonnet-4-5-20250929 | 200 |  | 0 | 0 | 0 | 0 |  note=Claude Sonnet 4.5 created=2025-09-29 max_input=1000000 max_output=64000 |
| reasoning/claude-opus-5-5/A-tool-call-0 | claude-opus-5-5 | 200 | tool_use | 498 | 174 | 0 | 0 | thinking(text=91 sig=860) text("Primes below 20: 2, 3, 5, 7, 11, 13, 17, 19. That makes 8, and the 8th Greek let…") tool_use(lookup map[key:theta]) transforms=<absent> |
| count/claude-opus-5-5/A | claude-opus-5-5 | 200 |  | 0 | 0 | 0 | 0 |  note=count_tokens=498 billed_input_total=498 delta=0 |
| reasoning/claude-opus-5-5/R1-replay-unchanged | claude-opus-5-5 | 200 | end_turn | 678 | 93 | 0 | 0 | thinking(text=77 sig=868) text("The key is \"theta\" and its value is 41.\n\n41 × 17 + 3 = 697 + 3 = 700; 700 mod 2…") transforms=<absent> |
| count/claude-opus-5-5/R1 | claude-opus-5-5 | 200 |  | 0 | 0 | 0 | 0 |  note=count_tokens=678 billed_input_total=678 delta=0 |
| reasoning/claude-opus-5-5/R1-replay-unchanged-header | claude-opus-5-5 | 200 | end_turn | 678 | 105 | 0 | 0 | thinking(text=72 sig=868) text("The key is \"theta\" and the lookup returned **41**.\n\n(41 × 17 + 3) mod 23 = (697…") transforms=[] |
| reasoning/claude-opus-5-5/R2-drop-reasoning | claude-opus-5-5 | 200 | end_turn | 604 | 95 | 0 | 0 | thinking(text=70 sig=784) text("The key is \"theta\" and lookup returned 41.\n\n41 × 17 + 3 = 697 + 3 = 700; 700 mo…") transforms=<absent> |
| reasoning/claude-opus-5-5/R2-drop-reasoning-dropblock | claude-opus-5-5 | 200 | end_turn | 604 | 103 | 0 | 0 | thinking(text=72 sig=784) text("The key is \"theta\", and it holds the value 41.\n\n(41 × 17 + 3) mod 23 = (697 + 3…") transforms=[] |
| reasoning/claude-opus-5-5/R3-modify-thinking-text | claude-opus-5-5 | 200 | end_turn | 678 | 101 | 0 | 0 | thinking(text=73 sig=868) text("The key is \"theta\" and its value is 41.\n\n(41 × 17 + 3) mod 23 = (697 + 3) mod 2…") transforms=<absent> |
| reasoning/claude-opus-5-5/R3-tamper-signature | claude-opus-5-5 | 400 |  | 0 | 0 | 0 | 0 |  error=invalid_request_error: messages.1.content.0: Invalid `signature` in `thinking` block |
| reasoning/claude-opus-5-5/R3-tamper-signature-dropblock | claude-opus-5-5 | 400 |  | 0 | 0 | 0 | 0 |  error=invalid_request_error: messages.1.content.0: Invalid `signature` in `thinking` block |
| reasoning/claude-opus-5-5/R4-rewrite-u0-noheader | claude-opus-5-5 | 200 | end_turn | 683 | 102 | 0 | 0 | thinking(text=66 sig=868) text("The lookup for \"theta\" returned 41, so the answer is **10**.\n\n41 × 17 + 3 = 697…") transforms=<absent> |
| reasoning/claude-opus-5-5/R4-rewrite-u0-header-unset | claude-opus-5-5 | 200 | end_turn | 683 | 101 | 0 | 0 | thinking(text=71 sig=868) text("The lookup for **theta** returned 41.\n\n(41 × 17 + 3) mod 23 = (697 + 3) mod 23 …") transforms=[{"type":"thinking_mismatch_allowed","path":"messages.1.content.0","reason":"prefix_binding_mismatch"}] |
| reasoning/claude-opus-5-5/R4-rewrite-u0-error | claude-opus-5-5 | 400 |  | 0 | 0 | 0 | 0 |  error=invalid_request_error: messages.1.content.0: Invalid `signature` in `thinking` block. The block is bound to a different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` to "drop_block". Content before this block differs from when it was created, first at `messages.0.content.0`. |
| reasoning/claude-opus-5-5/R4-rewrite-u0-dropblock | claude-opus-5-5 | 200 | end_turn | 609 | 81 | 0 | 0 | thinking(text=80 sig=784) text("The key is **theta**, and its value is 41.\n\n(41 × 17 + 3) mod 23 = 700 mod 23 =…") transforms=[{"type":"thinking_dropped","path":"messages.1.content.0","reason":"prefix_binding_mismatch"}] |
| reasoning/claude-opus-5-5/R4-rewrite-system-dropblock | claude-opus-5-5 | 200 | end_turn | 610 | 103 | 0 | 0 | thinking(text=67 sig=784) text("The key is **theta**, and its value is **41**.\n\n(41 × 17 + 3) mod 23 = (697 + 3…") transforms=[{"type":"thinking_dropped","path":"messages.1.content.0","reason":"prefix_binding_mismatch"}] |
| reasoning/claude-opus-5-5/R4-rewrite-tools-dropblock | claude-opus-5-5 | 200 | end_turn | 613 | 102 | 0 | 0 | thinking(text=65 sig=784) text("The key is **theta**, and its value is 41.\n\n(41 × 17 + 3) mod 23 = (697 + 3) mo…") transforms=[{"type":"thinking_dropped","path":"messages.1.content.0","reason":"prefix_binding_mismatch"}] |
| reasoning/claude-opus-5-5/edit-APPEND-dropblock | claude-opus-5-5 | 200 | end_turn | 678 | 106 | 0 | 0 | thinking(text=79 sig=868) text("The key is `theta`, and the lookup returned **41**.\n\n(41 × 17 + 3) mod 23 = (69…") transforms=[] |
| reasoning/claude-opus-5-5/edit-APPEND_SYSTEM-dropblock | claude-opus-5-5 | 200 | end_turn | 695 | 129 | 0 | 0 | thinking(text=215 sig=1108) text("The key \"theta\" has the value 41.\n\n(41 × 17 + 3) mod 23 = 700 mod 23 = **10**") transforms=[] |
| reasoning/claude-opus-5-5/edit-MOVE_CACHE_MARKERS-dropblock | claude-opus-5-5 | 200 | end_turn | 678 | 69 | 0 | 0 | text("The key is **theta**, and its value is **41**.\n\n(41 × 17 + 3) mod 23 = (697 + 3…") transforms=[] |
| reasoning/claude-opus-5-5/edit-ADD_DEFERRED_TOOL-dropblock | claude-opus-5-5 | 200 | end_turn | 771 | 98 | 0 | 0 | thinking(text=72 sig=868) text("The key is **theta**, and the lookup returned **41**.\n\n41 × 17 + 3 = 697 + 3 = …") transforms=[] |
| reasoning/claude-opus-5-5/R5-prior-turn-reasoning-replayed | claude-opus-5-5 | 200 | tool_use | 793 | 48 | 0 | 0 | tool_use(lookup map[key:beta]) transforms=[] |
| count/claude-opus-5-5/R5-replayed | claude-opus-5-5 | 200 |  | 0 | 0 | 0 | 0 |  note=count_tokens=793 billed_input_total=793 delta=0 |
| reasoning/claude-opus-5-5/R5-prior-turn-reasoning-stripped | claude-opus-5-5 | 200 | tool_use | 685 | 48 | 0 | 0 | tool_use(lookup map[key:beta]) transforms=[] |
| reasoning/claude-opus-5-5/R5-compare | claude-opus-5-5 | 0 |  | 0 | 0 | 0 | 0 |  note=billed input replayed=793 stripped=685 delta=108 (thinking blocks replayed: 2) |
| reasoning/claude-opus-5-5/edit-DROP_LEADING_REASONING-dropblock | claude-opus-5-5 | 200 | end_turn | 773 | 75 | 0 | 0 | thinking(text=78 sig=864) text("The value for \"beta\" is 7.\n\n7 × 17 + 3 = 119 + 3 = 122; 122 mod 23 = 122 − 11…") transforms=[] |
| reasoning/claude-opus-5-5/model-switch-to-claude-sonnet-5 | claude-sonnet-5 | 200 | end_turn | 670 | 43 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 4") transforms=[{"type":"thinking_dropped","path":"messages.1.content.0","reason":"model_binding_mismatch"}] |
| cache/claude-opus-5-5/C1-calibration | claude-opus-5-5 | 200 |  | 0 | 0 | 0 | 0 |  note=count_tokens: request without system=18, system(0 lines)=15, 32.10 tokens/line |
| cache/claude-opus-5-5/C1-prefix-256 | claude-opus-5-5 | 200 | refusal | 295 | 0 | 0 | 0 |  transforms=<absent> |
| cache/claude-opus-5-5/C1-prefix-1072 | claude-opus-5-5 | 200 | end_turn | 16 | 4 | 1109 | 0 | text("OK") transforms=<absent> |
| cache/claude-opus-5-5/C1-prefix-664 | claude-opus-5-5 | 200 | end_turn | 16 | 4 | 676 | 0 | text("OK") transforms=<absent> |
| cache/claude-opus-5-5/C1-prefix-460 | claude-opus-5-5 | 200 | end_turn | 509 | 4 | 0 | 0 | text("OK") transforms=<absent> |
| cache/claude-opus-5-5/C1-prefix-562 | claude-opus-5-5 | 200 | end_turn | 16 | 4 | 570 | 0 | text("OK") transforms=<absent> |
| cache/claude-opus-5-5/C1-prefix-511 | claude-opus-5-5 | 200 | end_turn | 16 | 4 | 531 | 0 | text("OK") transforms=<absent> |
| cache/claude-opus-5-5/C1-prefix-485 | claude-opus-5-5 | 200 | end_turn | 527 | 4 | 0 | 0 | text("OK") transforms=<absent> |
| cache/claude-opus-5-5/C1-prefix-498 | claude-opus-5-5 | 200 | end_turn | 521 | 4 | 0 | 0 | text("OK") transforms=<absent> |
| cache/claude-opus-5-5/C1-prefix-504 | claude-opus-5-5 | 200 | end_turn | 16 | 4 | 517 | 0 | text("OK") transforms=<absent> |
| cache/claude-opus-5-5/C1-prefix-501 | claude-opus-5-5 | 200 | end_turn | 527 | 4 | 0 | 0 | text("OK") transforms=<absent> |
| cache/claude-opus-5-5/C1-bracket | claude-opus-5-5 | 0 |  | 0 | 0 | 0 | 0 |  note=largest uncached request: billed input 527; smallest cached: billed input 533 (cache write 517, uncached tail 16); documented minimum 512 |
| cache/claude-opus-5-5/C2-identical-repeat | claude-opus-5-5 | 200 | end_turn | 16 | 4 | 0 | 517 | text("OK") transforms=<absent> |
| count/claude-opus-5-5/C2 | claude-opus-5-5 | 200 |  | 0 | 0 | 0 | 0 |  note=count_tokens=533 billed_input_total=533 delta=0 |
| cache/claude-opus-5-5/C3-1-write | claude-opus-5-5 | 200 | max_tokens | 4 | 64 | 1025 | 0 | thinking(text=0 sig=880) text("I've got it. Notebook **20260926T130130-") transforms=<absent> |
| cache/claude-opus-5-5/C3-2-append | claude-opus-5-5 | 200 | end_turn | 4 | 23 | 24 | 1025 | thinking(text=0 sig=736) text("**31**") transforms=<absent> |
| cache/claude-opus-5-5/C3-3-edit-early | claude-opus-5-5 | 200 | end_turn | 4 | 21 | 1050 | 0 | thinking(text=0 sig=736) text("31") transforms=<absent> |
| cache/claude-opus-5-5/C3-4-append-system | claude-opus-5-5 | 200 | end_turn | 2 | 21 | 15 | 1049 | thinking(text=0 sig=740) text("31") transforms=<absent> |
| cache/claude-opus-5-5/C1-prefix-512-ttl1h | claude-opus-5-5 | 200 | end_turn | 16 | 4 | 539 | 0 | text("OK") transforms=<absent> |
| compaction/claude-opus-5-5/K1-on-demand-summarize | claude-opus-5-5 | 200 | compaction | 828 | 294 | 0 | 0 | compaction(content=647 encrypted=0 sig=1736) transforms=<absent> iterations=[{"input_tokens":828,"output_tokens":294,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"compaction"}] |
| compaction/claude-opus-5-5/K2-adopt-restore-user | claude-opus-5-5 | 200 | end_turn | 762 | 223 | 0 | 0 | thinking(text=269 sig=1144) text("The lookup for \"theta\" returned **41 (0x29)**, and the final result is **10 (0xA…") transforms=<absent> iterations=[{"input_tokens":762,"output_tokens":223,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message"}] |
| compaction/claude-opus-5-5/K2-adopt-restore-system | claude-opus-5-5 | 200 | end_turn | 762 | 219 | 0 | 0 | thinking(text=196 sig=1236) text("The lookup for \"theta\" returned **41 (0x29)**, and the final result is **10 (0xA…") transforms=<absent> iterations=[{"input_tokens":762,"output_tokens":219,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message"}] |
| compaction/claude-opus-5-5/K3-tamper-summary | claude-opus-5-5 | 400 |  | 0 | 0 | 0 | 0 |  error=invalid_request_error: messages.0.content.0: `compaction` block `content` does not match its `signature` details={"error_code":"compaction_content_mismatch"} |
| compaction/claude-opus-5-5/K-kept-turns-thinking-dropblock | claude-opus-5-5 | 200 | end_turn | 773 | 113 | 0 | 0 | thinking(text=35 sig=888) text("The lookup for \"beta\" returned 7, and the formula gives **7**.\n\n(7 × 17 + 3) mo…") transforms=[] iterations=[{"input_tokens":773,"output_tokens":113,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message"}] |
| compaction/claude-opus-5-5/T1-threshold-trigger-1000 | claude-opus-5-5 | 400 |  | 0 | 0 | 0 | 0 |  error=invalid_request_error: context_management.edits.0.compact_20260112: trigger.value must be at least 50000 |
| compaction/claude-opus-5-5/T2-threshold-pause | claude-opus-5-5 | 200 | compaction | 51924 | 602 | 0 | 0 | compaction(content=942 encrypted=0 sig=0) transforms=<absent> iterations=[{"input_tokens":51924,"output_tokens":602,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"compaction"}] |
| compaction/claude-opus-5-5/T3-threshold-continue-restored | claude-opus-5-5 | 200 | end_turn | 919 | 128 | 0 | 0 | thinking(text=319 sig=920) text("**62** (0x3E)\n\nBed letters run A to Z in order of note number, so there are 61 f…") transforms=<absent> iterations=[{"input_tokens":919,"output_tokens":128,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"message"}] |
| compaction/claude-opus-5-5/CE1-clear-tool-uses | claude-opus-5-5 | 200 | end_turn | 847 | 78 | 0 | 0 | thinking(text=97 sig=864) text("The value for \"beta\" is 7.\n\n7 × 17 + 3 = 119 + 3 = 122; 122 mod 23 = 122 − 11…") transforms=[] |
| compaction/claude-opus-5-5/CE1-applied-edits | claude-opus-5-5 | 0 |  | 0 | 0 | 0 | 0 |  note={"applied_edits":[]} |
| compaction/claude-opus-5-5/CE2-clear-thinking | claude-opus-5-5 | 200 | end_turn | 737 | 87 | 0 | 0 | thinking(text=73 sig=880) text("The key \"beta\" has the value 7.\n\n7 × 17 + 3 = 119 + 3 = 122; 122 mod 23 = 122 \xe2…") transforms=[] |
| compaction/claude-opus-5-5/CE2-applied-edits | claude-opus-5-5 | 0 |  | 0 | 0 | 0 | 0 |  note={"applied_edits":[{"type":"clear_thinking_20251015","cleared_thinking_turns":2,"cleared_input_tokens":44}]} |
| reasoning/claude-sonnet-5/A-tool-call-0 | claude-sonnet-5 | 200 | tool_use | 564 | 143 | 0 | 0 | thinking(text=117 sig=528) tool_use(lookup map[key:theta]) transforms=<absent> |
| count/claude-sonnet-5/A | claude-sonnet-5 | 200 |  | 0 | 0 | 0 | 0 |  note=count_tokens=564 billed_input_total=564 delta=0 |
| reasoning/claude-sonnet-5/R1-replay-unchanged | claude-sonnet-5 | 200 | end_turn | 712 | 63 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 700 - 30*23(690) = 10\n\n**…") transforms=<absent> |
| count/claude-sonnet-5/R1 | claude-sonnet-5 | 200 |  | 0 | 0 | 0 | 0 |  note=count_tokens=712 billed_input_total=712 delta=0 |
| reasoning/claude-sonnet-5/R1-replay-unchanged-header | claude-sonnet-5 | 200 | end_turn | 712 | 43 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 22") transforms=[] |
| reasoning/claude-sonnet-5/R2-drop-reasoning | claude-sonnet-5 | 200 | end_turn | 618 | 162 | 0 | 0 | thinking(text=177 sig=520) text("Key: theta (8th Greek letter, since there are 8 primes below 20) → value = 41\n…") transforms=<absent> |
| reasoning/claude-sonnet-5/R2-drop-reasoning-dropblock | claude-sonnet-5 | 200 | end_turn | 618 | 183 | 0 | 0 | thinking(text=149 sig=520) text("Number of primes below 20 = 8 (2,3,5,7,11,13,17,19) → 8th Greek letter = theta…") transforms=[] |
| reasoning/claude-sonnet-5/R3-modify-thinking-text | claude-sonnet-5 | 200 | end_turn | 712 | 43 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 8") transforms=<absent> |
| reasoning/claude-sonnet-5/R3-tamper-signature | claude-sonnet-5 | 400 |  | 0 | 0 | 0 | 0 |  error=invalid_request_error: messages.1.content.0: Invalid `signature` in `thinking` block |
| reasoning/claude-sonnet-5/R3-tamper-signature-dropblock | claude-sonnet-5 | 400 |  | 0 | 0 | 0 | 0 |  error=invalid_request_error: messages.1.content.0: Invalid `signature` in `thinking` block |
| reasoning/claude-sonnet-5/R4-rewrite-u0-noheader | claude-sonnet-5 | 200 | end_turn | 717 | 43 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 8") transforms=<absent> |
| reasoning/claude-sonnet-5/R4-rewrite-u0-header-unset | claude-sonnet-5 | 200 | end_turn | 717 | 43 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 8") transforms=[] |
| reasoning/claude-sonnet-5/R4-rewrite-u0-error | claude-sonnet-5 | 200 | end_turn | 717 | 43 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 8") transforms=[] |
| reasoning/claude-sonnet-5/R4-rewrite-u0-dropblock | claude-sonnet-5 | 200 | end_turn | 717 | 43 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 5") transforms=[] |
| reasoning/claude-sonnet-5/R4-rewrite-system-dropblock | claude-sonnet-5 | 200 | end_turn | 718 | 43 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 10") transforms=[] |
| reasoning/claude-sonnet-5/R4-rewrite-tools-dropblock | claude-sonnet-5 | 200 | end_turn | 721 | 63 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 700 - 30*23(690) = 10\n\n**…") transforms=[] |
| reasoning/claude-sonnet-5/edit-APPEND-dropblock | claude-sonnet-5 | 200 | end_turn | 712 | 43 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 4") transforms=[] |
| reasoning/claude-sonnet-5/edit-APPEND_SYSTEM-dropblock | claude-sonnet-5 | 200 | end_turn | 729 | 44 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 8.") transforms=[] |
| reasoning/claude-sonnet-5/edit-MOVE_CACHE_MARKERS-dropblock | claude-sonnet-5 | 200 | end_turn | 712 | 63 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 700 - 30*23(690) = 10\n\n**…") transforms=[] |
| reasoning/claude-sonnet-5/edit-ADD_DEFERRED_TOOL-dropblock | claude-sonnet-5 | 200 | end_turn | 805 | 43 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 8") transforms=[] |
| reasoning/claude-sonnet-5/R5-prior-turn-reasoning-replayed | claude-sonnet-5 | 200 | tool_use | 795 | 58 | 0 | 0 | thinking(text=44 sig=356) tool_use(lookup map[key:beta]) transforms=[] |
| count/claude-sonnet-5/R5-replayed | claude-sonnet-5 | 200 |  | 0 | 0 | 0 | 0 |  note=count_tokens=795 billed_input_total=795 delta=0 |
| reasoning/claude-sonnet-5/R5-prior-turn-reasoning-stripped | claude-sonnet-5 | 200 | tool_use | 701 | 48 | 0 | 0 | tool_use(lookup map[key:beta]) transforms=[] |
| reasoning/claude-sonnet-5/R5-compare | claude-sonnet-5 | 0 |  | 0 | 0 | 0 | 0 |  note=billed input replayed=795 stripped=701 delta=94 (thinking blocks replayed: 1) |
| reasoning/claude-sonnet-5/edit-DROP_LEADING_REASONING-dropblock | claude-sonnet-5 | 200 | end_turn | 764 | 63 | 0 | 0 | text("(7 * 17 + 3) mod 23 = (119 + 3) mod 23 = 122 mod 23 = 122 - 5*23(115) = 7\n\n**Res…") transforms=[] |
| cache/claude-sonnet-5/C1-calibration | claude-sonnet-5 | 200 |  | 0 | 0 | 0 | 0 |  note=count_tokens: request without system=16, system(0 lines)=15, 32.10 tokens/line |
| cache/claude-sonnet-5/C1-prefix-512 | claude-sonnet-5 | 200 | end_turn | 547 | 4 | 0 | 0 | text("OK") transforms=<absent> |
| cache/claude-sonnet-5/C1-prefix-2096 | claude-sonnet-5 | 200 | end_turn | 14 | 4 | 2130 | 0 | text("OK") transforms=<absent> |
| cache/claude-sonnet-5/C1-prefix-1304 | claude-sonnet-5 | 200 | end_turn | 14 | 4 | 1315 | 0 | text("OK") transforms=<absent> |
| cache/claude-sonnet-5/C1-prefix-908 | claude-sonnet-5 | 200 | end_turn | 956 | 4 | 0 | 0 | text("OK") transforms=<absent> |
| cache/claude-sonnet-5/C1-prefix-1106 | claude-sonnet-5 | 200 | end_turn | 14 | 4 | 1145 | 0 | text("OK") transforms=<absent> |
| cache/claude-sonnet-5/C1-prefix-1007 | claude-sonnet-5 | 200 | end_turn | 14 | 4 | 1043 | 0 | text("OK") transforms=<absent> |
| cache/claude-sonnet-5/C1-prefix-957 | claude-sonnet-5 | 200 | end_turn | 990 | 4 | 0 | 0 | text("OK") transforms=<absent> |
| cache/claude-sonnet-5/C1-prefix-982 | claude-sonnet-5 | 200 | end_turn | 1006 | 4 | 0 | 0 | text("OK") transforms=<absent> |
| cache/claude-sonnet-5/C1-prefix-994 | claude-sonnet-5 | 200 | end_turn | 1030 | 4 | 0 | 0 | text("OK") transforms=<absent> |
| cache/claude-sonnet-5/C1-prefix-1000 | claude-sonnet-5 | 200 | end_turn | 14 | 4 | 1029 | 0 | text("OK") transforms=<absent> |
| cache/claude-sonnet-5/C1-prefix-997 | claude-sonnet-5 | 200 | end_turn | 1036 | 4 | 0 | 0 | text("OK") transforms=<absent> |
| cache/claude-sonnet-5/C1-bracket | claude-sonnet-5 | 0 |  | 0 | 0 | 0 | 0 |  note=largest uncached request: billed input 1036; smallest cached: billed input 1043 (cache write 1029, uncached tail 14); documented minimum 1024 |
| cache/claude-sonnet-5/C2-identical-repeat | claude-sonnet-5 | 200 | end_turn | 14 | 4 | 0 | 1029 | text("OK") transforms=<absent> |
| count/claude-sonnet-5/C2 | claude-sonnet-5 | 200 |  | 0 | 0 | 0 | 0 |  note=count_tokens=1043 billed_input_total=1043 delta=0 |
| cache/claude-sonnet-5/C3-1-write | claude-sonnet-5 | 200 | max_tokens | 2 | 64 | 2052 | 0 | text("I've reviewed and stored the details of Garden notebook 20260926T130313-C3, cont…") transforms=<absent> |
| cache/claude-sonnet-5/C3-2-append | claude-sonnet-5 | 200 | end_turn | 2 | 3 | 24 | 2052 | text("63") transforms=<absent> |
| cache/claude-sonnet-5/C3-3-edit-early | claude-sonnet-5 | 200 | end_turn | 2 | 24 | 2077 | 0 | text("There are **63** garden notes (numbered 0000 through 0062).") transforms=<absent> |
| cache/claude-sonnet-5/C3-4-append-system | claude-sonnet-5 | 200 | end_turn | 2 | 3 | 14 | 2076 | text("63") transforms=<absent> |
| cache/claude-sonnet-5/C1-prefix-1008-ttl1h | claude-sonnet-5 | 200 | end_turn | 14 | 4 | 1051 | 0 | text("OK") transforms=<absent> |
| compaction/claude-sonnet-5/K1-on-demand-summarize | claude-sonnet-5 | 200 | compaction | 832 | 230 | 0 | 0 | compaction(content=533 encrypted=0 sig=636) transforms=<absent> iterations=[{"input_tokens":832,"output_tokens":230,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":0},"type":"compaction"}] |
