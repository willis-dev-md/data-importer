# Factory usage report

Ledger: `factory/metrics/usage.jsonl`: 86 rows, 1 session(s). Usage is deduplicated per API request. **Cost: n/a: fill `factory/metrics/prices.json` (per-million-token prices for each model) to see dollars.** Shares below use fresh tokens (input + cache writes + output).

`cache read` is cheap per token but large; it is shown separately and not counted in 'fresh'.

## Per task, split by role

### Task 001: 5 launches, rounds 1, verdicts PASS→PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| worker · code · claude-sonnet-5-5 | 1 | 18 | 36 | 40,183 | 510,928 | 16,751 | 33% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 20 | 58 | 19,912 | 7,667,182 | 10,219 | 17% | n/a |
| validator · validate · claude-sonnet-5-5 | 1 | 6 | 12 | 25,782 | 97,060 | 2,762 | 16% | n/a |
| runbook-refiner · runbook · claude-sonnet-5-5 | 1 | 8 | 16 | 19,667 | 149,648 | 5,107 | 14% | n/a |
| validator · docs-check · claude-sonnet-5-5 | 1 | 6 | 12 | 15,577 | 90,368 | 2,492 | 10% | n/a |
| documentor · docs · claude-sonnet-5-5 | 1 | 5 | 10 | 13,689 | 71,061 | 2,389 | 9% | n/a |

### Task 002: 5 launches, rounds 1, verdicts PASS→PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| worker · code · claude-sonnet-5-5 | 1 | 8 | 16 | 30,152 | 190,065 | 10,251 | 24% | n/a |
| runbook-refiner · runbook · claude-sonnet-5-5 | 1 | 10 | 20 | 27,898 | 232,369 | 7,448 | 21% | n/a |
| validator · validate · claude-sonnet-5-5 | 1 | 5 | 10 | 24,448 | 84,272 | 2,181 | 16% | n/a |
| validator · docs-check · claude-sonnet-5-5 | 1 | 9 | 18 | 19,586 | 171,158 | 3,568 | 14% | n/a |
| documentor · docs · claude-sonnet-5-5 | 1 | 5 | 10 | 18,728 | 85,467 | 2,631 | 13% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 14 | 42 | 14,772 | 5,698,593 | 5,682 | 12% | n/a |

### Task 003: 7 launches, rounds 2, verdicts PASS→PASS→PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| worker · code · claude-sonnet-5-5 | 2 | 16 | 32 | 80,714 | 480,723 | 38,490 | 42% | n/a |
| validator · validate · claude-sonnet-5-5 | 2 | 15 | 30 | 60,155 | 328,767 | 10,528 | 25% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 20 | 59 | 25,105 | 8,615,819 | 11,598 | 13% | n/a |
| validator · docs-check · claude-sonnet-5-5 | 1 | 6 | 12 | 19,719 | 97,068 | 2,306 | 8% | n/a |
| documentor · docs · claude-sonnet-5-5 | 1 | 6 | 12 | 18,909 | 103,365 | 2,382 | 8% | n/a |
| runbook-refiner · runbook · claude-sonnet-5-5 | 1 | 3 | 6 | 12,428 | 34,759 | 1,049 | 5% | n/a |

### Task 004: 3 launches, rounds 1, verdicts PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| worker · code · claude-sonnet-5-5 | 1 | 15 | 30 | 52,395 | 466,476 | 26,225 | 49% | n/a |
| validator · validate · claude-sonnet-5-5 | 1 | 13 | 26 | 39,277 | 349,960 | 7,884 | 29% | n/a |
| documentor · docs · claude-sonnet-5-5 | 1 | 5 | 10 | 17,164 | 81,557 | 2,152 | 12% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 8 | 25 | 11,881 | 3,914,145 | 3,226 | 9% | n/a |

### Task 005: 3 launches, rounds 1, verdicts PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| worker · code · claude-sonnet-5-5 | 1 | 11 | 22 | 36,336 | 296,966 | 11,285 | 48% | n/a |
| validator · validate · claude-sonnet-5-5 | 1 | 5 | 10 | 19,895 | 82,863 | 2,789 | 23% | n/a |
| documentor · docs · claude-sonnet-5-5 | 1 | 5 | 10 | 15,353 | 75,553 | 1,655 | 17% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 7 | 21 | 8,940 | 3,506,435 | 2,871 | 12% | n/a |

### Task 006: 4 launches, rounds 1, verdicts PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| worker · code · claude-sonnet-5-5 | 1 | 14 | 28 | 68,098 | 625,916 | 26,429 | 40% | n/a |
| runbook-refiner · runbook · claude-sonnet-5-5 | 1 | 13 | 26 | 34,784 | 359,669 | 11,259 | 20% | n/a |
| documentor · docs · claude-sonnet-5-5 | 1 | 6 | 12 | 31,517 | 141,278 | 6,393 | 16% | n/a |
| validator · validate · claude-sonnet-5-5 | 1 | 8 | 16 | 32,066 | 189,102 | 4,829 | 16% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 9 | 29 | 13,565 | 4,623,179 | 5,144 | 8% | n/a |

### Task 007: 4 launches, rounds 1, verdicts PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| worker · code · claude-sonnet-5-5 | 1 | 14 | 28 | 78,739 | 759,372 | 41,803 | 47% | n/a |
| validator · validate · claude-sonnet-5-5 | 1 | 9 | 18 | 34,512 | 193,667 | 7,270 | 16% | n/a |
| runbook-refiner · runbook · claude-sonnet-5-5 | 1 | 6 | 12 | 33,587 | 143,724 | 6,137 | 15% | n/a |
| documentor · docs · claude-sonnet-5-5 | 1 | 7 | 14 | 34,072 | 197,683 | 4,610 | 15% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 9 | 29 | 12,268 | 4,760,676 | 5,124 | 7% | n/a |

### Task 008: 7 launches, rounds 3, verdicts PASS→PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| validator · validate · claude-sonnet-5-5 | 2 | 23 | 46 | 158,979 | 404,664 | 11,244 | 34% | n/a |
| worker · code · claude-sonnet-5-5 | 3 | 33 | 66 | 134,836 | 918,642 | 29,868 | 33% | n/a |
| runbook-refiner · runbook · claude-sonnet-5-5 | 1 | 12 | 24 | 54,270 | 405,455 | 12,231 | 13% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 31 | 91 | 39,661 | 17,320,631 | 23,972 | 13% | n/a |
| documentor · docs · claude-sonnet-5-5 | 1 | 5 | 10 | 34,800 | 92,833 | 3,484 | 8% | n/a |

### Task 009: 4 launches, rounds 1, verdicts PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| worker · code · claude-sonnet-5-5 | 1 | 20 | 40 | 106,945 | 1,456,377 | 39,654 | 47% | n/a |
| documentor · docs · claude-sonnet-5-5 | 1 | 13 | 26 | 46,783 | 480,515 | 10,455 | 18% | n/a |
| validator · validate · claude-sonnet-5-5 | 1 | 15 | 30 | 47,375 | 468,209 | 8,484 | 18% | n/a |
| runbook-refiner · runbook · claude-sonnet-5-5 | 1 | 22 | 44 | 40,923 | 725,445 | 14,273 | 18% | n/a |

### Task 010: 6 launches, rounds 2, verdicts FAIL→PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| worker · code · claude-sonnet-5-5 | 2 | 23 | 46 | 71,722 | 622,570 | 25,168 | 32% | n/a |
| validator · validate · claude-sonnet-5-5 | 2 | 24 | 48 | 69,846 | 607,346 | 12,016 | 27% | n/a |
| runbook-refiner · runbook · claude-sonnet-5-5 | 1 | 19 | 38 | 37,906 | 541,443 | 14,914 | 18% | n/a |
| documentor · docs · claude-sonnet-5-5 | 1 | 11 | 22 | 43,101 | 332,758 | 8,689 | 17% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 11 | 37 | 11,459 | 6,689,617 | 5,147 | 6% | n/a |

### Task 011: 7 launches, rounds 3, verdicts FAIL→PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 23 | 62 | 611,776 | 14,275,376 | 14,018 | 47% | n/a |
| worker · code · claude-sonnet-5-5 | 2 | 44 | 88 | 189,990 | 2,384,096 | 55,583 | 18% | n/a |
| runbook-refiner · runbook · claude-haiku-5-5 | 1 | 43 | 86 | 105,981 | 2,826,507 | 59,389 | 12% | n/a |
| documentor · docs · claude-haiku-5-5 | 1 | 40 | 80 | 107,281 | 2,912,184 | 47,214 | 12% | n/a |
| validator · validate · claude-sonnet-5-5 | 2 | 28 | 56 | 95,596 | 874,486 | 21,494 | 9% | n/a |
| documentor · docs · claude-sonnet-5-5 | 1 | 5 | 10 | 18,455 | 64,216 | 1,705 | 2% | n/a |
| orchestrator · orchestrate · <synthetic> | - | 4 | 0 | 0 | 0 | 0 | 0% | n/a |

### Task 012: 4 launches, rounds 1, verdicts PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| documentor · docs · claude-haiku-5-5 | 1 | 25 | 50 | 86,952 | 1,425,801 | 38,757 | 35% | n/a |
| runbook-refiner · runbook · claude-haiku-5-5 | 1 | 27 | 54 | 80,710 | 1,475,734 | 42,672 | 35% | n/a |
| worker · code · claude-sonnet-5-5 | 1 | 8 | 16 | 34,742 | 201,169 | 10,111 | 13% | n/a |
| validator · validate · claude-sonnet-5-5 | 1 | 16 | 32 | 35,375 | 402,352 | 7,357 | 12% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 10 | 29 | 12,930 | 6,732,582 | 4,688 | 5% | n/a |

### Task 013: 6 launches, rounds 3, verdicts FAIL→FAIL→PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| worker · code · claude-sonnet-5-5 | 1 | 80 | 164 | 643,673 | 12,234,636 | 146,061 | 61% | n/a |
| validator · validate · claude-sonnet-5-5 | 3 | 73 | 146 | 196,778 | 3,173,173 | 72,819 | 21% | n/a |
| runbook-refiner · runbook · claude-haiku-5-5 | 1 | 10 | 20 | 72,608 | 450,442 | 35,351 | 8% | n/a |
| documentor · docs · claude-haiku-5-5 | 1 | 7 | 14 | 58,060 | 244,063 | 15,706 | 6% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 22 | 67 | 32,706 | 15,577,505 | 15,462 | 4% | n/a |

### Task 018: 2 launches, rounds 2, verdicts n/a

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| worker · code · claude-sonnet-5-5 | 1 | 35 | 72 | 158,890 | 2,046,702 | 36,812 | 80% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 7 | 21 | 17,737 | 5,148,822 | 8,112 | 11% | n/a |
| validator · validate · claude-sonnet-5-5 | 1 | 2 | 4 | 21,785 | 12,173 | 401 | 9% | n/a |

### Task shared: 0 launches, rounds 0, verdicts n/a

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 17 | 53 | 24,875 | 10,002,107 | 9,518 | 100% | n/a |

### Task spike: 2 launches, rounds 1, verdicts n/a

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Explore · code · claude-haiku-5-5 | 1 | 3 | 6 | 59,657 | 114,132 | 540 | 63% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 6 | 16 | 20,102 | 1,839,050 | 7,130 | 28% | n/a |
| documentor · docs · claude-haiku-5-5 | 1 | 1 | 2 | 8,675 | 0 | 186 | 9% | n/a |

### Task unattributed: 0 launches, rounds 0, verdicts n/a

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 246 | 551 | 917,329 | 63,343,670 | 475,842 | 100% | n/a |

## Cross-task review: where tokens go and which refinement it points to

**Fresh tokens by role** (all tasks)

| role | launches | fresh tokens | share | cache read | est. cost |
|---|---:|---:|---:|---:|---:|
| orchestrator | - | 2,403,961 | 33% | 179,715,389 | n/a |
| worker | 19 | 2,242,590 | 31% | 23,194,638 | n/a |
| validator | 23 | 1,097,701 | 15% | 7,626,688 | n/a |
| runbook-refiner | 11 | 730,938 | 10% | 7,345,195 | n/a |
| documentor | 15 | 702,239 | 10% | 6,308,334 | n/a |
| Explore | 1 | 60,203 | 1% | 114,132 | n/a |

**Launches per task:** average 4.8 (max 7). Each launch pays a cold-start prefix (below); fewer launches is the biggest lever.

**Launches the spec said were unnecessary (`none: <reason>`)**: refinement 1

| task | role | launches | fresh tokens |
|---|---|---:|---:|
| 003 | runbook-refiner | 1 | 13,483 |

**Second validator pass (phase docs-check):** 3 launches, 63,290 fresh tokens vs 1,034,411 for the code validations: refinement 2

**Gate output read by agents:** 172,655 characters (~43,163 tokens, 4 chars/token estimate) across 45 agents: refinement 3

**Longest agent reports (words) and orchestrator share:** refinement 4

- task 013 validator validate: 669 words, 20,635 output tokens
- task 011 validator validate: 668 words, 11,539 output tokens
- task 013 validator validate: 598 words, 36,521 output tokens
- orchestrator: 2,403,961 fresh tokens, 179,715,389 cache-read tokens (33% of all fresh tokens)

**Role × model with quality guard** (refinement 5: only downgrade a model where rework does not rise)

| role | model | launches | avg fresh/launch | PASS | FAIL | UNVERIFIED mentions |
|---|---|---:|---:|---:|---:|---:|
| Explore | claude-haiku-5-5 | 1 | 60,203 | 0 | 0 | 0 |
| documentor | claude-haiku-5-5 | 4 | 90,744 | 0 | 0 | 3 |
| documentor | claude-sonnet-5-5 | 11 | 30,842 | 0 | 0 | 10 |
| runbook-refiner | claude-haiku-5-5 | 3 | 132,290 | 0 | 0 | 4 |
| runbook-refiner | claude-sonnet-5-5 | 8 | 41,758 | 0 | 0 | 8 |
| validator | claude-sonnet-5-5 | 23 | 47,726 | 18 | 4 | 2 |
| worker | claude-sonnet-5-5 | 19 | 118,031 | 0 | 0 | 0 |

**Rework (rounds above 1 on code/validate):** better specs and test-linked criteria: refinement 6

| task | rounds | rework fresh tokens | share of task |
|---|---:|---:|---:|
| 003 | 2 | 72,681 | 29% |
| 008 | 3 | 225,410 | 51% |
| 010 | 2 | 90,746 | 32% |
| 011 | 3 | 215,550 | 31% |
| 013 | 3 | 184,743 | 15% |
| 018 | 2 | 22,190 | 10% |

**Same file read by 3+ agents in one task** (section-scoped refs or summaries): refinement 8

- /home/user/data-importer/factory/tasks/003-store-and-record.md: 5 agents (task 003)
- /home/user/data-importer/factory/tasks/009-cli-sources-run.md: 4 agents (task 009)
- /home/user/data-importer/internal/importer/importer.go: 4 agents (task 008)
- /home/user/data-importer/factory/tasks/008-importer-core.md: 4 agents (task 008)
- /home/user/data-importer/factory/tasks/002-schema-migrations.md: 4 agents (task 002)
- /home/user/data-importer/.claude/agents/validator.md: 3 agents (task 013)
- /home/user/data-importer/factory/tasks/011-data-service.md: 3 agents (task 011)
- /home/user/data-importer/factory/tasks/010-grpc-skeleton.md: 3 agents (task 010)

**Docs-role budget** (documentor 20 tool calls, runbook-refiner 25): refinement 'cap docs roles'

| task | role | tool calls | budget | fresh tokens |
|---|---|---:|---:|---:|
| 009 | documentor | 24 | 20 | 57,264 |
| 011 | documentor | 48 | 20 | 154,575 |
| 011 | runbook-refiner | 50 | 25 | 165,456 |
| 012 | documentor | 30 | 20 | 125,759 |
| 012 | runbook-refiner | 29 | 25 | 123,436 |

Docs roles used 1,433,177 fresh tokens vs 3,340,291 for worker + validator (43%).

**Cold-start prefix and cache efficiency per role** (refinements 10 and 'restrict tools')

| role | median prefix tokens | cache-read ratio |
|---|---:|---:|
| Explore | 55,012 | 66% |
| documentor | 12,297 | 92% |
| runbook-refiner | 12,472 | 93% |
| validator | 11,715 | 89% |
| worker | 11,663 | 93% |

_Attribution notes: orchestrator requests are assigned to the task whose agent-launch window contains them; requests between tasks are `unattributed`, overlapping windows are `shared`._
