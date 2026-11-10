> An earlier run, kept for comparison. In this version of the guard, a turn in which every tool call failed counted towards the notice sent to the model. See the study README for why that was dropped.

Run on 2026-11-10 through Bifrost with the session guard's default settings. 3 trials per row, at most 24 turns each. "Dry run" is the same gateway and plugin with the guard only reporting, so it is the baseline. In dry run, "first notice" is the turn on which the guard would have acted.

## Plain agent: it ends when the model stops calling tools

### Polling a job that never finishes

| Model | Guard | What happened in each trial | Average turns | Average tokens |
| --- | --- | --- | --- | --- |
| claude-haiku-5-5 | dry run | stopped by itself on turn 6 (first notice on turn 5); stopped by itself on turn 6 (first notice on turn 5); stopped by itself on turn 6 (first notice on turn 5) | 6.0 | 4,900 |
| claude-haiku-5-5 | guard on | stopped by itself on turn 4; stopped by itself on turn 5; stopped by itself on turn 5 | 4.7 | 3,504 |
| claude-sonnet-5-5 | dry run | stopped by itself on turn 8 (first notice on turn 5); stopped by itself on turn 8 (first notice on turn 5); stopped by itself on turn 8 (first notice on turn 5) | 8.0 | 6,720 |
| claude-sonnet-5-5 | guard on | stopped by itself on turn 5; stopped by itself on turn 5; stopped by itself on turn 5 | 5.0 | 3,613 |

### Reading a file that does not exist

| Model | Guard | What happened in each trial | Average turns | Average tokens |
| --- | --- | --- | --- | --- |
| claude-haiku-5-5 | dry run | stopped by itself on turn 5; stopped by itself on turn 5; stopped by itself on turn 6 | 5.3 | 4,925 |
| claude-haiku-5-5 | guard on | stopped by itself on turn 5; stopped by itself on turn 5; stopped by itself on turn 5 | 5.0 | 4,571 |
| claude-sonnet-5-5 | dry run | stopped by itself on turn 4; stopped by itself on turn 4; stopped by itself on turn 4 | 4.0 | 3,593 |
| claude-sonnet-5-5 | guard on | stopped by itself on turn 4; stopped by itself on turn 4; stopped by itself on turn 4 | 4.0 | 3,698 |

### Fixing a test that never passes

| Model | Guard | What happened in each trial | Average turns | Average tokens |
| --- | --- | --- | --- | --- |
| claude-haiku-5-5 | dry run | stopped by itself on turn 13 (first notice on turn 11); stopped by itself on turn 13 (first notice on turn 6); stopped by itself on turn 15 (first notice on turn 6) | 13.7 | 39,780 |
| claude-haiku-5-5 | guard on | stopped by itself on turn 6; stopped by itself on turn 8; stopped by itself on turn 9 | 7.7 | 13,075 |
| claude-sonnet-5-5 | dry run | stopped by itself on turn 13 (first notice on turn 10); stopped by itself on turn 13 (first notice on turn 10); stopped by itself on turn 18 (first notice on turn 12) | 14.7 | 36,702 |
| claude-sonnet-5-5 | guard on | stopped by itself on turn 6; stopped by itself on turn 6; stopped by itself on turn 10 | 7.3 | 9,839 |

Across these scenarios: 0 of 18 dry-run sessions and 0 of 18 guarded sessions ran to the turn limit. Average per session: 8.6 turns and 16,103 tokens in dry run, 5.6 turns and 6,383 tokens with the guard on.

## Persistent harness: every attempt to stop is answered with "Continue. The task is not finished yet."

### Polling a job that never finishes

| Model | Guard | What happened in each trial | Average turns | Average tokens |
| --- | --- | --- | --- | --- |
| claude-haiku-5-5 | dry run | ran to the turn limit (first notice on turn 5); ran to the turn limit (first notice on turn 7); ran to the turn limit (first notice on turn 5) | 24.0 | 48,977 |
| claude-haiku-5-5 | guard on | blocked by the guard on turn 10 (first notice on turn 5); blocked by the guard on turn 10 (first notice on turn 5); blocked by the guard on turn 10 (first notice on turn 5) | 10.0 | 10,924 |
| claude-sonnet-5-5 | dry run | ran to the turn limit (first notice on turn 5); ran to the turn limit (first notice on turn 5); ran to the turn limit (first notice on turn 5) | 24.0 | 42,159 |
| claude-sonnet-5-5 | guard on | blocked by the guard on turn 10 (first notice on turn 5); blocked by the guard on turn 10 (first notice on turn 5); blocked by the guard on turn 10 (first notice on turn 5) | 10.0 | 9,105 |

### Reading a file that does not exist

| Model | Guard | What happened in each trial | Average turns | Average tokens |
| --- | --- | --- | --- | --- |
| claude-haiku-5-5 | dry run | ran to the turn limit (first notice on turn 8); ran to the turn limit (first notice on turn 8); ran to the turn limit (first notice on turn 8) | 24.0 | 81,465 |
| claude-haiku-5-5 | guard on | blocked by the guard on turn 13 (first notice on turn 8); blocked by the guard on turn 13 (first notice on turn 8); blocked by the guard on turn 14 (first notice on turn 9) | 13.3 | 22,572 |
| claude-sonnet-5-5 | dry run | ran to the turn limit (first notice on turn 8); ran to the turn limit (first notice on turn 8); ran to the turn limit (first notice on turn 7) | 24.0 | 86,566 |
| claude-sonnet-5-5 | guard on | blocked by the guard on turn 12 (first notice on turn 8); blocked by the guard on turn 12 (first notice on turn 7); blocked by the guard on turn 12 (first notice on turn 7) | 12.0 | 23,367 |

### Fixing a test that never passes

| Model | Guard | What happened in each trial | Average turns | Average tokens |
| --- | --- | --- | --- | --- |
| claude-haiku-5-5 | dry run | ran to the turn limit (first notice on turn 6); ran to the turn limit (first notice on turn 6); ran to the turn limit (first notice on turn 6) | 24.0 | 119,969 |
| claude-haiku-5-5 | guard on | blocked by the guard on turn 15 (first notice on turn 6); blocked by the guard on turn 17 (first notice on turn 6); blocked by the guard on turn 19 (first notice on turn 6) | 17.0 | 55,527 |
| claude-sonnet-5-5 | dry run | ran to the turn limit (first notice on turn 6); ran to the turn limit (first notice on turn 10); ran to the turn limit (first notice on turn 10) | 24.0 | 108,278 |
| claude-sonnet-5-5 | guard on | blocked by the guard on turn 21 (first notice on turn 10); blocked by the guard on turn 23 (first notice on turn 10); blocked by the guard on turn 21 (first notice on turn 6) | 21.7 | 82,485 |

Across these scenarios: 18 of 18 dry-run sessions and 0 of 18 guarded sessions ran to the turn limit. Average per session: 24.0 turns and 81,236 tokens in dry run, 14.0 turns and 33,997 tokens with the guard on.

## Controls: tasks that can be finished

The guard should do nothing here. A notice in this table is the guard acting on a task that could be finished.

| Task | Model | Guard | What happened in each trial | Task finished | Guard notices |
| --- | --- | --- | --- | --- | --- |
| polling a job that does finish | claude-haiku-5-5 | dry run | stopped by itself on turn 8; stopped by itself on turn 8; stopped by itself on turn 8 | 3 of 3 | 0 |
| polling a job that does finish | claude-haiku-5-5 | guard on | stopped by itself on turn 8; stopped by itself on turn 8; stopped by itself on turn 8 | 3 of 3 | 0 |
| polling a job that does finish | claude-sonnet-5-5 | dry run | stopped by itself on turn 8; stopped by itself on turn 8; stopped by itself on turn 8 | 3 of 3 | 0 |
| polling a job that does finish | claude-sonnet-5-5 | guard on | stopped by itself on turn 8; stopped by itself on turn 8; stopped by itself on turn 8 | 3 of 3 | 0 |
| fixing a test that can be fixed | claude-haiku-5-5 | dry run | stopped by itself on turn 4; stopped by itself on turn 4; stopped by itself on turn 9 (first notice on turn 6) | 3 of 3 | 2 |
| fixing a test that can be fixed | claude-haiku-5-5 | guard on | stopped by itself on turn 4; stopped by itself on turn 6; stopped by itself on turn 6 | 1 of 3 | 2 |
| fixing a test that can be fixed | claude-sonnet-5-5 | dry run | stopped by itself on turn 4; stopped by itself on turn 5; stopped by itself on turn 4 | 3 of 3 | 0 |
| fixing a test that can be fixed | claude-sonnet-5-5 | guard on | stopped by itself on turn 4; stopped by itself on turn 5; stopped by itself on turn 7 | 3 of 3 | 0 |

