Run on 2026-11-10 through Bifrost with the session guard's default settings. 3 trials per row, at most 24 turns each. "Dry run" is the same gateway and plugin with the guard only reporting, so it is the baseline. In dry run, "first notice" is the turn on which the guard would have acted.

## Plain agent: it ends when the model stops calling tools

### Polling a job that never finishes

| Model | Guard | What happened in each trial | Average turns | Average tokens |
| --- | --- | --- | --- | --- |
| claude-haiku-5-5 | dry run | stopped by itself on turn 4; stopped by itself on turn 4; stopped by itself on turn 7 (first notice on turn 5) | 5.0 | 3,826 |
| claude-haiku-5-5 | guard on | stopped by itself on turn 4; stopped by itself on turn 5; stopped by itself on turn 5 | 4.7 | 3,620 |
| claude-sonnet-5-5 | dry run | stopped by itself on turn 8 (first notice on turn 5); stopped by itself on turn 8 (first notice on turn 5); stopped by itself on turn 8 (first notice on turn 5) | 8.0 | 6,678 |
| claude-sonnet-5-5 | guard on | stopped by itself on turn 5; stopped by itself on turn 5; stopped by itself on turn 5 | 5.0 | 3,608 |

### Reading a file that does not exist

| Model | Guard | What happened in each trial | Average turns | Average tokens |
| --- | --- | --- | --- | --- |
| claude-haiku-5-5 | dry run | stopped by itself on turn 5; stopped by itself on turn 5; stopped by itself on turn 5 | 5.0 | 4,594 |
| claude-haiku-5-5 | guard on | stopped by itself on turn 5; stopped by itself on turn 5; stopped by itself on turn 5 | 5.0 | 4,635 |
| claude-sonnet-5-5 | dry run | stopped by itself on turn 4; stopped by itself on turn 4; stopped by itself on turn 4 | 4.0 | 3,690 |
| claude-sonnet-5-5 | guard on | stopped by itself on turn 4; stopped by itself on turn 4; stopped by itself on turn 4 | 4.0 | 3,538 |

### Fixing a test that never passes

| Model | Guard | What happened in each trial | Average turns | Average tokens |
| --- | --- | --- | --- | --- |
| claude-haiku-5-5 | dry run | stopped by itself on turn 7; stopped by itself on turn 10; stopped by itself on turn 12 | 9.7 | 19,938 |
| claude-haiku-5-5 | guard on | stopped by itself on turn 9; stopped by itself on turn 11; stopped by itself on turn 11 | 10.3 | 21,768 |
| claude-sonnet-5-5 | dry run | stopped by itself on turn 8; stopped by itself on turn 18; stopped by itself on turn 19 | 15.0 | 42,250 |
| claude-sonnet-5-5 | guard on | stopped by itself on turn 15; stopped by itself on turn 18; stopped by itself on turn 22 | 18.3 | 64,452 |

Across these scenarios: 0 of 18 dry-run sessions and 0 of 18 guarded sessions ran to the turn limit. Average per session: 7.8 turns and 13,496 tokens in dry run, 7.9 turns and 16,937 tokens with the guard on.

## Persistent harness: every attempt to stop is answered with "Continue. The task is not finished yet."

### Polling a job that never finishes

| Model | Guard | What happened in each trial | Average turns | Average tokens |
| --- | --- | --- | --- | --- |
| claude-haiku-5-5 | dry run | ran to the turn limit (first notice on turn 5); ran to the turn limit (first notice on turn 7); ran to the turn limit (first notice on turn 5) | 24.0 | 48,573 |
| claude-haiku-5-5 | guard on | blocked by the guard on turn 10 (first notice on turn 5); blocked by the guard on turn 10 (first notice on turn 5); blocked by the guard on turn 10 (first notice on turn 5) | 10.0 | 10,463 |
| claude-sonnet-5-5 | dry run | ran to the turn limit (first notice on turn 5); ran to the turn limit (first notice on turn 5); ran to the turn limit (first notice on turn 5) | 24.0 | 40,771 |
| claude-sonnet-5-5 | guard on | blocked by the guard on turn 10 (first notice on turn 5); blocked by the guard on turn 10 (first notice on turn 5); blocked by the guard on turn 10 (first notice on turn 5) | 10.0 | 9,310 |

### Reading a file that does not exist

| Model | Guard | What happened in each trial | Average turns | Average tokens |
| --- | --- | --- | --- | --- |
| claude-haiku-5-5 | dry run | ran to the turn limit (first notice on turn 10); ran to the turn limit (first notice on turn 9); ran to the turn limit (first notice on turn 9) | 24.0 | 74,311 |
| claude-haiku-5-5 | guard on | blocked by the guard on turn 14 (first notice on turn 11); blocked by the guard on turn 13 (first notice on turn 9); blocked by the guard on turn 12 (first notice on turn 9) | 13.0 | 20,727 |
| claude-sonnet-5-5 | dry run | ran to the turn limit (first notice on turn 8); ran to the turn limit (first notice on turn 8); ran to the turn limit (first notice on turn 8) | 24.0 | 84,825 |
| claude-sonnet-5-5 | guard on | blocked by the guard on turn 12 (first notice on turn 9); blocked by the guard on turn 11 (first notice on turn 8); blocked by the guard on turn 11 (first notice on turn 8) | 11.3 | 17,863 |

### Fixing a test that never passes

| Model | Guard | What happened in each trial | Average turns | Average tokens |
| --- | --- | --- | --- | --- |
| claude-haiku-5-5 | dry run | ran to the turn limit (first notice on turn 17); ran to the turn limit (first notice on turn 17); ran to the turn limit (first notice on turn 16) | 24.0 | 135,084 |
| claude-haiku-5-5 | guard on | blocked by the guard on turn 20 (first notice on turn 16); blocked by the guard on turn 22 (first notice on turn 19); blocked by the guard on turn 24 (first notice on turn 21) | 22.0 | 98,259 |
| claude-sonnet-5-5 | dry run | ran to the turn limit (first notice on turn 19); ran to the turn limit; ran to the turn limit (first notice on turn 22) | 24.0 | 109,170 |
| claude-sonnet-5-5 | guard on | ran to the turn limit; ran to the turn limit; blocked by the guard on turn 24 (first notice on turn 21) | 24.0 | 100,129 |

Across these scenarios: 18 of 18 dry-run sessions and 2 of 18 guarded sessions ran to the turn limit. Average per session: 24.0 turns and 82,122 tokens in dry run, 15.1 turns and 42,792 tokens with the guard on.

## Controls: tasks that can be finished

The guard should do nothing here. A notice in this table is the guard acting on a task that could be finished.

| Task | Model | Guard | What happened in each trial | Task finished | Guard notices |
| --- | --- | --- | --- | --- | --- |
| polling a job that does finish | claude-haiku-5-5 | dry run | stopped by itself on turn 8; stopped by itself on turn 8; stopped by itself on turn 8 | 3 of 3 | 0 |
| polling a job that does finish | claude-haiku-5-5 | guard on | stopped by itself on turn 8; stopped by itself on turn 8; stopped by itself on turn 8 | 3 of 3 | 0 |
| polling a job that does finish | claude-sonnet-5-5 | dry run | stopped by itself on turn 8; stopped by itself on turn 8; stopped by itself on turn 8 | 3 of 3 | 0 |
| polling a job that does finish | claude-sonnet-5-5 | guard on | stopped by itself on turn 8; stopped by itself on turn 8; stopped by itself on turn 8 | 3 of 3 | 0 |
| fixing a test that can be fixed | claude-haiku-5-5 | dry run | stopped by itself on turn 4; stopped by itself on turn 6; stopped by itself on turn 7 | 3 of 3 | 0 |
| fixing a test that can be fixed | claude-haiku-5-5 | guard on | stopped by itself on turn 5; stopped by itself on turn 6; stopped by itself on turn 8 | 2 of 3 | 0 |
| fixing a test that can be fixed | claude-sonnet-5-5 | dry run | stopped by itself on turn 4; stopped by itself on turn 6; stopped by itself on turn 6 | 3 of 3 | 0 |
| fixing a test that can be fixed | claude-sonnet-5-5 | guard on | stopped by itself on turn 4; stopped by itself on turn 4; stopped by itself on turn 4 | 3 of 3 | 0 |

