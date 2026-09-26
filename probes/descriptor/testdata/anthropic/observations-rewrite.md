# Anthropic descriptor probe: raw observations

Generated 2026-09-26T13:08:50Z by probes/descriptor/anthropic. Estimated spend $0.0338.

| probe | model | status | stop | in | out | cache w | cache r | blocks / transformations / error / note |
|---|---|---|---|---|---|---|---|---|
| rewrite/claude-opus-5-5/setup-a1 | claude-opus-5-5 | 200 | tool_use | 498 | 123 | 0 | 0 | thinking(text=98 sig=860) tool_use(lookup map[key:theta]) transforms=<absent> |
| rewrite/claude-opus-5-5/setup-a3 | claude-opus-5-5 | 200 | end_turn | 627 | 156 | 0 | 0 | thinking(text=71 sig=864) text("There are 8 primes below 20 (2, 3, 5, 7, 11, 13, 17, 19), and the 8th Greek lett…") transforms=<absent> |
| rewrite/claude-opus-5-5/setup-a5 | claude-opus-5-5 | 200 | tool_use | 805 | 48 | 0 | 0 | tool_use(lookup map[key:beta]) transforms=<absent> |
| rewrite/claude-opus-5-5/RW1-rewrite-u0-strip-after | claude-opus-5-5 | 200 | end_turn | 755 | 72 | 0 | 0 | thinking(text=68 sig=772) text("The lookup for beta returned **7**.\n\n(7 × 17 + 3) mod 23 = 122 mod 23 = 122 −…") transforms=[] |
| rewrite/claude-opus-5-5/RW2-rewrite-u4-keep-before-strip-after | claude-opus-5-5 | 200 | end_turn | 864 | 71 | 0 | 0 | thinking(text=56 sig=860) text("Looking up beta returned **7**.\n\n(7 × 17 + 3) mod 23 = 122 mod 23 = 122 − 115…") transforms=[] |
| rewrite/claude-opus-5-5/RW3-rewrite-u4-replay-after | claude-opus-5-5 | 0 |  | 0 | 0 | 0 | 0 |  note=a5 carried no thinking; control not applicable |
| rewrite/claude-sonnet-5/setup-a1 | claude-sonnet-5 | 200 | tool_use | 564 | 95 | 0 | 0 | thinking(text=101 sig=428) tool_use(lookup map[key:theta]) transforms=<absent> |
| rewrite/claude-sonnet-5/setup-a3 | claude-sonnet-5 | 200 | end_turn | 664 | 64 | 0 | 0 | text("(41 * 17 + 3) mod 23 = (697 + 3) mod 23 = 700 mod 23 = 700 - 30*23(690) = 10\n\n**…") transforms=<absent> |
| rewrite/claude-sonnet-5/setup-a5 | claude-sonnet-5 | 200 | tool_use | 748 | 48 | 0 | 0 | tool_use(lookup map[key:beta]) transforms=<absent> |
| rewrite/claude-sonnet-5/RW1-rewrite-u0-strip-after | claude-sonnet-5 | 200 | end_turn | 761 | 64 | 0 | 0 | text("(7 * 17 + 3) mod 23 = (119 + 3) mod 23 = 122 mod 23 = 122 - 5*23(115) = 7\n\n**Ans…") transforms=[] |
| rewrite/claude-sonnet-5/RW2-rewrite-u4-keep-before-strip-after | claude-sonnet-5 | 200 | end_turn | 807 | 44 | 0 | 0 | text("(7 * 17 + 3) mod 23 = (119 + 3) mod 23 = 122 mod 23 = **7**") transforms=[] |
| rewrite/claude-sonnet-5/RW3-rewrite-u4-replay-after | claude-sonnet-5 | 0 |  | 0 | 0 | 0 | 0 |  note=a5 carried no thinking; control not applicable |
