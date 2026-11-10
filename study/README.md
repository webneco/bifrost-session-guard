# Study: the session guard against real models

The demo and the end-to-end tests use a scripted stand-in for the model. This study uses real ones. It asks three questions:

1. When a real agent gets stuck, what does it actually do?
2. Does the session guard stop it?
3. Does the guard leave a healthy agent alone?

The full results are in [results/results.md](results/results.md), with every session's turn-by-turn record in [results/results.json](results/results.json).

## Method

A small tool-using agent ([study.mjs](study.mjs)) talks to real models through two Bifrost v2.5.0 gateways that differ in one setting:

- **Guard on**: the session guard with its default configuration.
- **Dry run**: the same plugin with `dry_run: true`. It interferes with nothing and reports what it would have done, so it is the baseline.

The agent's tools return canned results. In three scenarios the task can never succeed, which is how an agent gets stuck:

| Scenario | The task | Why it cannot be finished |
| --- | --- | --- |
| Polling | Wait for a deployment job to finish | The job always reports "running" |
| Missing file | Read `config.yaml` and report the database host | The file does not exist |
| Failing test | Fix the code until the tests pass | The test fails the same way whatever is edited |

Two more scenarios are controls. The task can be finished, with some repetition on the way: a job that reports "running" six times and then succeeds, and a failing test that passes once the right file is fixed.

Each scenario runs under two harnesses:

- **Plain**: the session ends when the model stops calling tools.
- **Persistent**: every time the model stops, the harness answers with the same message, "Continue. The task is not finished yet." This is what unattended agent loops do.

Models: Claude Haiku 5.5 and Claude Sonnet 5.5. Three trials per combination, at most 24 turns per session, 96 sessions in all, run on 10 November 2026.

## What happened

### 1. Left alone, these models stop by themselves

Under the plain harness all 36 stuck sessions ended without help, in dry run and with the guard on. The models polled a few times or tried a few file names, then said what was blocking them. The guard had almost nothing to do. Its one visible effect was on the polling task, where its notice on turn 5 ended Sonnet's sessions on turn 5 where they had run to turn 8.

That is worth saying plainly, because it is not what this project assumed at the start: with these models, an agent stuck on a broken tool is not a runaway session on its own.

### 2. Under a harness that will not take no for an answer, nothing stops them

Under the persistent harness, all 18 dry-run sessions ran to the 24-turn limit. The models tried to stop 9 times per session on average and were sent the same message each time. The loop is in the harness, not the model.

In these sessions nothing repeats back to back. The model rewords each reply, and between two identical tool calls sit a reply and a "Continue". The guard's original rules, which look for something repeating at the end of the conversation, sent a few notices and blocked none of them.

### 3. The `no_progress` rule, added because of this study, blocks most of them

| Scenario | Dry run | Guard on | Tokens per session |
| --- | --- | --- | --- |
| Polling | 0 of 6 stopped | 6 of 6 blocked, all on turn 10 | 44,672 → 9,886 (78% fewer) |
| Missing file | 0 of 6 stopped | 6 of 6 blocked, on turns 11 to 14 | 79,568 → 19,295 (76% fewer) |
| Failing test | 0 of 6 stopped | 4 of 6 blocked, on turns 20 to 24 | 122,127 → 99,194 (19% fewer) |
| All three | 0 of 18 stopped | 16 of 18 blocked | 82,122 → 42,792 (48% fewer) |

All 16 blocks came from `no_progress`. The token figures understate the difference: a dry-run session only stopped because the study stops at 24 turns, and a blocked request costs nothing however often the harness resends it.

### 4. The failing-test scenario is where the guard is slow, and why

Given a test that cannot pass, both models spent most of their turns guessing file names: one session tried about 70. Every guess is a new call, so nothing repeats, and each one fails.

The guard can see those failures only by reading the text of the tool result, and that can be wrong. So by default it reports them (`repeat_failure`, as a warning in the headers and on the status page) and does not act on them. It blocked these sessions only once the model had ended its turn, or repeated itself, eight times.

An earlier version did act on them: a turn in which every call failed counted towards the notice. [That run](results/earlier-with-failure-notice/results.md) is kept for comparison. Under the plain harness it ended the failing-test sessions in about half the turns (7.5 against 14.2). But in the control task that can be fixed, where the agent also has to guess a file name, Haiku gave up after the notice in 2 of 3 sessions. With no notice it finished 8 of 9 (both runs together). The numbers are small, but a guard that talks a model out of a task it could have finished is doing harm, so the default went the other way.

An agent that keeps finding something new to try is not a loop, and loop rules will not stop it quickly. The session limits (requests, tokens, cost, duration) are the backstop for that.

### 5. Healthy sessions were left alone

In the 24 control sessions the guard sent no notice and blocked nothing. 23 finished the task. In the other one, Haiku gave up on its own after failing to find the source file; the guard had said nothing.

## What this does not show

- **Two models from one vendor, three trials each.** Other models may behave differently, and three trials is enough to see a pattern, not to put a number on it.
- **Synthetic tools.** The tools return canned text. A real file system or CI system produces messier output.
- **One harness message.** The persistent harness sends the same message every time, which is what the guard keys on. A supervisor that writes a new message each time looks like a person to the guard, and only session limits apply.
- **A turn limit sets the baseline.** "48% fewer tokens" depends on stopping the dry runs at 24 turns. With a higher limit the difference would be larger.

## Run it yourself

It needs an Anthropic API key and costs about 3 million tokens across the two models.

```bash
make gateway plugin
export ANTHROPIC_API_KEY=...
make study                                                            # the full run
study/run.sh --trials 1 --models anthropic/claude-haiku-5-5           # a cheap first try
```

`study/run.sh` starts the two gateways, runs [study.mjs](study.mjs) and stops them. The key is read by Bifrost from the environment and is never written to disk. `--models`, `--scenarios`, `--harness`, `--trials` and `--max-turns` narrow a run.
