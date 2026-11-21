# Factory usage report

Ledger: `factory/metrics/usage.jsonl`: 24 rows, 1 session(s). Usage is deduplicated per API request. **Cost: n/a: fill `factory/metrics/prices.json` (per-million-token prices for each model) to see dollars.** Shares below use fresh tokens (input + cache writes + output).

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

### Task spike: 2 launches, rounds 1, verdicts n/a

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| Explore · code · claude-haiku-5-5 | 1 | 3 | 6 | 59,657 | 114,132 | 540 | 63% | n/a |
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 6 | 16 | 20,102 | 1,839,050 | 7,130 | 28% | n/a |
| documentor · docs · claude-haiku-5-5 | 1 | 1 | 2 | 8,675 | 0 | 186 | 9% | n/a |

### Task unattributed: 0 launches, rounds 0, verdicts n/a

| role · phase · model | launches | requests | input | cache write | cache read | output | share | est. cost |
|---|---:|---:|---:|---:|---:|---:|---:|---:|
| orchestrator · orchestrate · claude-sonnet-5-5 | - | 214 | 477 | 864,710 | 44,642,772 | 424,085 | 100% | n/a |

## Cross-task review: where tokens go and which refinement it points to

**Fresh tokens by role** (all tasks)

| role | launches | fresh tokens | share | cache read | est. cost |
|---|---:|---:|---:|---:|---:|
| orchestrator | - | 1,403,967 | 70% | 68,463,416 | n/a |
| worker | 4 | 216,625 | 11% | 1,181,716 | n/a |
| validator | 7 | 189,198 | 9% | 868,693 | n/a |
| runbook-refiner | 3 | 73,639 | 4% | 416,776 | n/a |
| documentor | 4 | 67,623 | 3% | 259,893 | n/a |
| Explore | 1 | 60,203 | 3% | 114,132 | n/a |

**Launches per task:** average 5.7 (max 7). Each launch pays a cold-start prefix (below); fewer launches is the biggest lever.

**Launches the spec said were unnecessary (`none: <reason>`)**: refinement 1

| task | role | launches | fresh tokens |
|---|---|---:|---:|
| 003 | runbook-refiner | 1 | 13,483 |

**Second validator pass (phase docs-check):** 3 launches, 63,290 fresh tokens vs 125,908 for the code validations: refinement 2

**Gate output read by agents:** 26,584 characters (~6,646 tokens, 4 chars/token estimate) across 12 agents: refinement 3

**Longest agent reports (words) and orchestrator share:** refinement 4

- task 003 validator validate: 490 words, 4,874 output tokens
- task 003 validator validate: 403 words, 5,654 output tokens
- task 002 validator docs-check: 384 words, 3,568 output tokens
- orchestrator: 1,403,967 fresh tokens, 68,463,416 cache-read tokens (70% of all fresh tokens)

**Role × model with quality guard** (refinement 5: only downgrade a model where rework does not rise)

| role | model | launches | avg fresh/launch | PASS | FAIL | UNVERIFIED mentions |
|---|---|---:|---:|---:|---:|---:|
| Explore | claude-haiku-5-5 | 1 | 60,203 | 0 | 0 | 0 |
| documentor | claude-haiku-5-5 | 1 | 8,863 | 0 | 0 | 0 |
| documentor | claude-sonnet-5-5 | 3 | 19,586 | 0 | 0 | 3 |
| runbook-refiner | claude-sonnet-5-5 | 3 | 24,546 | 0 | 0 | 3 |
| validator | claude-sonnet-5-5 | 7 | 27,028 | 7 | 0 | 2 |
| worker | claude-sonnet-5-5 | 4 | 54,156 | 0 | 0 | 0 |

**Rework (rounds above 1 on code/validate):** better specs and test-linked criteria: refinement 6

| task | rounds | rework fresh tokens | share of task |
|---|---:|---:|---:|
| 003 | 2 | 72,681 | 29% |

**Same file read by 3+ agents in one task** (section-scoped refs or summaries): refinement 8

- /home/user/data-importer/factory/tasks/003-store-and-record.md: 5 agents (task 003)
- /home/user/data-importer/factory/tasks/002-schema-migrations.md: 4 agents (task 002)
- /home/user/data-importer/internal/record/record.go: 3 agents (task 003)
- /home/user/data-importer/.claude/agents/validator.md: 3 agents (task 003)

**Cold-start prefix and cache efficiency per role** (refinements 10 and 'restrict tools')

| role | median prefix tokens | cache-read ratio |
|---|---:|---:|
| Explore | 55,012 | 66% |
| documentor | 11,098 | 81% |
| runbook-refiner | 11,286 | 87% |
| validator | 10,611 | 84% |
| worker | 10,815 | 89% |

_Attribution notes: orchestrator requests are assigned to the task whose agent-launch window contains them; requests between tasks are `unattributed`, overlapping windows are `shared`._
