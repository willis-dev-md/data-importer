# Factory usage report

Ledger: `factory/metrics/usage.jsonl`: 46 rows, 1 session(s). Usage is deduplicated per API request. **Cost: n/a: fill `factory/metrics/prices.json` (per-million-token prices for each model) to see dollars.** Shares below use fresh tokens (input + cache writes + output).

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

### Task 008: 3 launches, rounds 2, verdicts PASS

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| worker · code · claude-sonnet-5-5 | 2 | 21 | 42 | 77,873 | 674,828 | 25,113 | 66% | n/a |
| validator · validate · claude-sonnet-5-5 | 1 | 8 | 16 | 32,898 | 187,777 | 5,628 | 25% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 7 | 21 | 9,986 | 3,795,656 | 4,052 | 9% | n/a |

### Task spike: 2 launches, rounds 1, verdicts n/a

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Explore · code · claude-haiku-5-5 | 1 | 3 | 6 | 59,657 | 114,132 | 540 | 63% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 6 | 16 | 20,102 | 1,839,050 | 7,130 | 28% | n/a |
| documentor · docs · claude-haiku-5-5 | 1 | 1 | 2 | 8,675 | 0 | 186 | 9% | n/a |

### Task unattributed: 0 launches, rounds 0, verdicts n/a

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 231 | 517 | 898,735 | 52,988,373 | 452,923 | 100% | n/a |

## Cross-task review: where tokens go and which refinement it points to

**Fresh tokens by role** (all tasks)

| role | launches | fresh tokens | share | cache read | est. cost |
|---|---:|---:|---:|---:|---:|
| orchestrator | - | 1,544,052 | 52% | 97,409,108 | n/a |
| worker | 10 | 661,071 | 22% | 4,005,274 | n/a |
| validator | 12 | 376,332 | 13% | 1,872,062 | n/a |
| documentor | 8 | 180,585 | 6% | 755,964 | n/a |
| runbook-refiner | 5 | 159,444 | 5% | 920,169 | n/a |
| Explore | 1 | 60,203 | 2% | 114,132 | n/a |

**Launches per task:** average 4.2 (max 7). Each launch pays a cold-start prefix (below); fewer launches is the biggest lever.

**Launches the spec said were unnecessary (`none: <reason>`)**: refinement 1

| task | role | launches | fresh tokens |
|---|---|---:|---:|
| 003 | runbook-refiner | 1 | 13,483 |

**Second validator pass (phase docs-check):** 3 launches, 63,290 fresh tokens vs 313,042 for the code validations: refinement 2

**Gate output read by agents:** 59,849 characters (~14,962 tokens, 4 chars/token estimate) across 23 agents: refinement 3

**Longest agent reports (words) and orchestrator share:** refinement 4

- task 004 validator validate: 502 words, 7,884 output tokens
- task 003 validator validate: 490 words, 4,874 output tokens
- task 008 validator validate: 470 words, 5,628 output tokens
- orchestrator: 1,544,052 fresh tokens, 97,409,108 cache-read tokens (52% of all fresh tokens)

**Role × model with quality guard** (refinement 5: only downgrade a model where rework does not rise)

| role | model | launches | avg fresh/launch | PASS | FAIL | UNVERIFIED mentions |
|---|---|---:|---:|---:|---:|---:|
| Explore | claude-haiku-5-5 | 1 | 60,203 | 0 | 0 | 0 |
| documentor | claude-haiku-5-5 | 1 | 8,863 | 0 | 0 | 0 |
| documentor | claude-sonnet-5-5 | 7 | 24,531 | 0 | 0 | 7 |
| runbook-refiner | claude-sonnet-5-5 | 5 | 31,888 | 0 | 0 | 5 |
| validator | claude-sonnet-5-5 | 12 | 31,361 | 12 | 0 | 2 |
| worker | claude-sonnet-5-5 | 10 | 66,107 | 0 | 0 | 0 |

**Rework (rounds above 1 on code/validate):** better specs and test-linked criteria: refinement 6

| task | rounds | rework fresh tokens | share of task |
|---|---:|---:|---:|
| 003 | 2 | 72,681 | 29% |
| 008 | 2 | 31,941 | 23% |

**Same file read by 3+ agents in one task** (section-scoped refs or summaries): refinement 8

- /home/user/data-importer/factory/tasks/003-store-and-record.md: 5 agents (task 003)
- /home/user/data-importer/factory/tasks/002-schema-migrations.md: 4 agents (task 002)
- /home/user/data-importer/factory/tasks/008-importer-core.md: 3 agents (task 008)
- /home/user/data-importer/factory/tasks/006-mapping-parse-validate.md: 3 agents (task 006)
- /home/user/data-importer/internal/record/record.go: 3 agents (task 003)
- /home/user/data-importer/.claude/agents/validator.md: 3 agents (task 003)

**Cold-start prefix and cache efficiency per role** (refinements 10 and 'restrict tools')

| role | median prefix tokens | cache-read ratio |
|---|---:|---:|
| Explore | 55,012 | 66% |
| documentor | 11,572 | 83% |
| runbook-refiner | 11,343 | 88% |
| validator | 10,683 | 85% |
| worker | 11,346 | 90% |

_Attribution notes: orchestrator requests are assigned to the task whose agent-launch window contains them; requests between tasks are `unattributed`, overlapping windows are `shared`._
