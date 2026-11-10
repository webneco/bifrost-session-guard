package guard

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---- helpers ----

type conv struct {
	items []Item
	n     int
}

func (c *conv) user(text string) *conv {
	c.items = append(c.items, Item{Kind: KindUser, Sig: SigString(text)})
	return c
}

func (c *conv) asst(text string) *conv {
	c.items = append(c.items, Item{Kind: KindAssistantText, Sig: SigString(text)})
	return c
}

// step adds a tool call and its result, with a fresh call id each time.
func (c *conv) step(tool, args, result string) *conv {
	c.n++
	id := fmt.Sprintf("call_%d", c.n)
	c.items = append(c.items,
		Item{Kind: KindToolCall, Sig: SigToolCall(tool, args), Name: tool, CallID: id},
		Item{Kind: KindToolResult, Sig: SigString(result), CallID: id, Head: Head(result)},
	)
	return c
}

// together adds several tool calls made in one model turn, then their results.
// Each entry is {tool, args, result}.
func (c *conv) together(calls ...[3]string) *conv {
	ids := make([]string, len(calls))
	for i, call := range calls {
		c.n++
		ids[i] = fmt.Sprintf("call_%d", c.n)
		c.items = append(c.items, Item{Kind: KindToolCall, Sig: SigToolCall(call[0], call[1]), Name: call[0], CallID: ids[i]})
	}
	for i, call := range calls {
		c.items = append(c.items, Item{Kind: KindToolResult, Sig: SigString(call[2]), CallID: ids[i], Head: Head(call[2])})
	}
	return c
}

func (c *conv) build() Conversation {
	root := uint64(0)
	if len(c.items) > 0 {
		root = c.items[0].Sig
	}
	return Conversation{Model: "test/model", Items: c.items, Total: len(c.items), RootSig: root}
}

func newConv() *conv { return (&conv{}).user("fix the failing test") }

func testEngine(t *testing.T, mutate func(*Config)) *Engine {
	t.Helper()
	cfg := DefaultConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	e := New(cfg)
	t.Cleanup(e.Close)
	return e
}

func req(id string, c Conversation) Request {
	return Request{SessionKey: "s1", SessionID: "s1", Source: "header", RequestID: id, Conv: c, CanNudge: true}
}

// ---- detection ----

func TestNoLoopOnVariedWork(t *testing.T) {
	c := newConv().
		step("read_file", `{"path":"a.go"}`, "package a").
		step("read_file", `{"path":"b.go"}`, "package b").
		step("run_tests", `{}`, "1 failed").
		step("edit_file", `{"path":"a.go"}`, "ok").
		step("run_tests", `{}`, "all passed")
	if f := detectLoops(elements(c.items), DefaultConfig().Loops); f != nil {
		t.Fatalf("unexpected finding: %+v", f)
	}
}

func TestRepeatToolCallTriggersAtThreshold(t *testing.T) {
	cfg := DefaultConfig().Loops
	c := newConv()
	for i := 1; i <= 3; i++ {
		c.step("read_file", `{"path":"missing.yaml"}`, "error: no such file")
	}
	if f := detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("3 repeats is under the threshold of 4, got %+v", f)
	}
	c.step("read_file", `{"path":"missing.yaml"}`, "error: no such file")
	f := detectLoops(elements(c.items), cfg)
	if f == nil || f.Rule != RuleRepeatToolCall || f.Count != 4 || f.Action != ActionNudge {
		t.Fatalf("want repeat_tool_call x4 nudge, got %+v", f)
	}
	if !strings.Contains(f.Reason(), "read_file") {
		t.Fatalf("reason should name the tool: %q", f.Reason())
	}
}

func TestRepeatToolCallEscalatesToBlock(t *testing.T) {
	cfg := DefaultConfig().Loops // threshold 4, escalate after 3
	c := newConv()
	for i := 1; i <= 7; i++ {
		c.step("read_file", `{"path":"missing.yaml"}`, "error: no such file")
	}
	f := detectLoops(elements(c.items), cfg)
	if f == nil || f.Action != ActionBlock || !f.Escalated || f.Count != 7 {
		t.Fatalf("want escalated block at 7 repeats, got %+v", f)
	}
}

func TestArgumentKeyOrderAndWhitespaceDoNotHideARepeat(t *testing.T) {
	c := newConv().
		step("search", `{"q":"x","limit":5}`, "none").
		step("search", `{"limit":5,"q":"x"}`, "none").
		step("search", `{ "q" : "x", "limit" : 5 }`, "none").
		step("search", `{"limit": 5, "q": "x"}`, "none")
	f := detectLoops(elements(c.items), DefaultConfig().Loops)
	if f == nil || f.Rule != RuleRepeatToolCall || f.Count != 4 {
		t.Fatalf("want repeat_tool_call x4, got %+v", f)
	}
}

func TestRewordedAssistantTextDoesNotHideARepeat(t *testing.T) {
	c := newConv()
	for i := 1; i <= 4; i++ {
		c.asst(fmt.Sprintf("Let me try that again, attempt %d.", i))
		c.step("read_file", `{"path":"missing.yaml"}`, "error: no such file")
	}
	f := detectLoops(elements(c.items), DefaultConfig().Loops)
	if f == nil || f.Rule != RuleRepeatToolCall || f.Count != 4 {
		t.Fatalf("want repeat_tool_call x4, got %+v", f)
	}
}

func TestSameCallDifferentResultsIsProgressUntilLooseThreshold(t *testing.T) {
	cfg := DefaultConfig().Loops // loose threshold 10
	c := newConv()
	for i := 1; i <= 9; i++ {
		c.step("check_status", `{}`, fmt.Sprintf("pending (request %d)", i))
	}
	if f := detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("9 differing results should not trigger, got %+v", f)
	}
	c.step("check_status", `{}`, "pending (request 10)")
	f := detectLoops(elements(c.items), cfg)
	if f == nil || f.Rule != RuleRepeatToolCallAnyResult || f.Count != 10 {
		t.Fatalf("want repeat_tool_call_any_result x10, got %+v", f)
	}
}

func TestIgnoredToolsMayRepeat(t *testing.T) {
	cfg := DefaultConfig().Loops
	cfg.IgnoreTools = []string{"wait"}
	c := newConv()
	for i := 1; i <= 12; i++ {
		c.step("wait", `{"seconds":5}`, "ok")
	}
	if f := detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("ignored tool should not trigger, got %+v", f)
	}
}

func TestToolCycleABAB(t *testing.T) {
	cfg := DefaultConfig().Loops // repeats 3
	c := newConv()
	for i := 1; i <= 2; i++ {
		c.step("edit_file", `{"path":"a.go","patch":"+x"}`, "ok").step("run_tests", `{}`, "1 failed: TestA")
	}
	if f := detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("2 rounds is under repeats=3, got %+v", f)
	}
	c.step("edit_file", `{"path":"a.go","patch":"+x"}`, "ok").step("run_tests", `{}`, "1 failed: TestA")
	f := detectLoops(elements(c.items), cfg)
	if f == nil || f.Rule != RuleToolCycle || f.Count != 3 || f.Period != 2 {
		t.Fatalf("want tool_cycle x3 period 2, got %+v", f)
	}
	if got := toolList(f.Tools); got != "edit_file, run_tests" {
		t.Fatalf("tools = %q", got)
	}
}

func TestCycleBrokenByNewWork(t *testing.T) {
	c := newConv()
	for i := 1; i <= 3; i++ {
		c.step("edit_file", `{"path":"a.go"}`, "ok").step("run_tests", `{}`, "1 failed")
	}
	c.step("read_file", `{"path":"go.mod"}`, "module x")
	if f := detectLoops(elements(c.items), DefaultConfig().Loops); f != nil {
		t.Fatalf("a different step at the end breaks the loop, got %+v", f)
	}
}

func TestHumanMessageBreaksALoop(t *testing.T) {
	c := newConv()
	for i := 1; i <= 6; i++ {
		c.step("read_file", `{"path":"missing.yaml"}`, "error: no such file")
	}
	c.user("the file is called config.yml, not missing.yaml")
	if f := detectLoops(elements(c.items), DefaultConfig().Loops); f != nil {
		t.Fatalf("a new user message should clear the loop, got %+v", f)
	}
}

// ---- loops that do not repeat back to back ----

func TestNoProgressSeesALoopThroughAHarnessSayingContinue(t *testing.T) {
	// What a real model did in the study: poll, report, get told to continue, poll again.
	// The model rewords its report each time, so nothing repeats back to back.
	cfg := DefaultConfig().Loops
	const push = "Continue. The task is not finished yet."
	c := newConv()
	for i := 1; i <= 2; i++ {
		c.step("check_job", `{"job_id":"42"}`, `{"status":"running"}`).
			asst(fmt.Sprintf("Still running after check %d. I will stop here.", i)).
			user(push)
	}
	// One new poll, one repeat of it, and two replies the harness would not accept.
	if f := detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("3 turns with nothing new is under the threshold of 5, got %+v", f)
	}
	c.step("check_job", `{"job_id":"42"}`, `{"status":"running"}`).asst("Check 3 says the same.").user(push)
	f := detectLoops(elements(c.items), cfg)
	if f == nil || f.Rule != RuleNoProgress || f.Count != 5 || f.Action != ActionNudge {
		t.Fatalf("want no_progress x5 nudge, got %+v", f)
	}
	if !strings.Contains(f.Reason(), "check_job") || !strings.Contains(f.NudgeText(""), "your last 5 turns") {
		t.Fatalf("reason and notice should name the tool and the count: %q / %q", f.Reason(), f.NudgeText(""))
	}
	c.step("check_job", `{"job_id":"42"}`, `{"status":"running"}`).asst("Check 4. Nothing has changed.").user(push).
		step("check_job", `{"job_id":"42"}`, `{"status":"running"}`)
	f = detectLoops(elements(c.items), cfg)
	if f == nil || f.Rule != RuleNoProgress || f.Count != 8 || f.Action != ActionBlock || !f.Escalated {
		t.Fatalf("want no_progress x8 escalated to block, got %+v", f)
	}
}

func TestNoProgressWhenTheModelStopsAndTheHarnessWillNotLetIt(t *testing.T) {
	// The other thing a real model did: after the guard's notice it stopped calling tools
	// and said so, in new words each time, and the harness kept sending the same message.
	cfg := DefaultConfig().Loops
	c := newConv().
		step("read_file", `{"path":"a.go"}`, "package a").
		step("edit_file", `{"path":"a.go"}`, "ok").
		step("run_tests", `{}`, "ok: 12 passed")
	for i := 1; i <= 5; i++ {
		c.asst(fmt.Sprintf("The work is complete (%d).", i)).user("Continue.")
	}
	f := detectLoops(elements(c.items), cfg)
	if f == nil || f.Rule != RuleNoProgress || f.Count != 5 || len(f.Tools) != 0 {
		t.Fatalf("want no_progress x5 with no tools, got %+v", f)
	}
	if !strings.Contains(f.Reason(), "ended the turn") || !strings.Contains(f.NudgeText(""), "last 5 replies") {
		t.Fatalf("reason and notice should describe the replies: %q / %q", f.Reason(), f.NudgeText(""))
	}
}

func TestNoProgressLeavesAChatWithNoToolsAlone(t *testing.T) {
	// A person asking for a long answer in parts. No tools, so this is not an agent.
	c := (&conv{}).user("Write a long story about a lighthouse.")
	for i := 1; i <= 9; i++ {
		c.asst(fmt.Sprintf("Part %d of the story.", i)).user("continue")
	}
	if f := detectLoops(elements(c.items), DefaultConfig().Loops); f != nil {
		t.Fatalf("a chat with no tool calls is not a stalled agent, got %+v", f)
	}
}

func TestNoProgressLeavesAHarnessLoopThatIsGettingWorkDoneAlone(t *testing.T) {
	// An autonomous loop that works: each time it is told to continue, the agent does
	// something it had not done before, and it succeeds.
	c := newConv()
	for i := 1; i <= 12; i++ {
		c.step("edit_file", fmt.Sprintf(`{"path":"f%d.go"}`, i), "ok").
			step("run_tests", `{}`, "ok: 12 passed").
			asst(fmt.Sprintf("Part %d is done.", i)).
			user("Continue.")
	}
	if f := detectLoops(elements(c.items), DefaultConfig().Loops); f != nil {
		t.Fatalf("new successful work between stops is progress, got %+v", f)
	}
}

func TestAFailedAttemptNeitherCountsNorResets(t *testing.T) {
	cfg := DefaultConfig().Loops
	// Guess a file name, fail, guess another: new calls every time, and none of them works.
	c := newConv().step("list_files", `{}`, "README.md")
	for i := 1; i <= 20; i++ {
		c.step("read_file", fmt.Sprintf(`{"path":"guess%d.yaml"}`, i), fmt.Sprintf("error: guess%d.yaml: no such file or directory", i))
	}
	// Failure is read from free text, so the model is not told and nothing is blocked. The
	// operator sees it.
	f := detectLoops(elements(c.items), cfg)
	if f == nil || f.Rule != RuleRepeatFailure || f.Action != ActionWarn || f.Count != 20 {
		t.Fatalf("want repeat_failure x20 as a warning only, got %+v", f)
	}
	cfg.RepeatFailure.Enabled = boolPtr(false)
	if f = detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("failed attempts are not counted by no_progress, got %+v", f)
	}
	// They do not reset it either: five replies the harness will not accept, with a failed
	// guess between each, are five turns with nothing new.
	for i := 21; i <= 25; i++ {
		c.asst(fmt.Sprintf("I cannot find the file (%d).", i)).user("Continue.").
			step("read_file", fmt.Sprintf(`{"path":"guess%d.yaml"}`, i), fmt.Sprintf("error: guess%d.yaml: no such file or directory", i))
	}
	if f = detectLoops(elements(c.items), cfg); f == nil || f.Rule != RuleNoProgress || f.Count != 5 || f.Action != ActionNudge {
		t.Fatalf("want no_progress x5, got %+v", f)
	}
	// One new call that works, and the count starts again.
	c.step("read_file", `{"path":"settings.yaml"}`, "host: db.internal")
	if f = detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("a new call that works is progress, got %+v", f)
	}
}

func TestNoProgressIgnoresRepeatsWithNewWorkBetween(t *testing.T) {
	// A healthy session: every edit is new, and the tests give the same answer each time.
	c := newConv()
	for i := 1; i <= 9; i++ {
		c.step("edit_file", fmt.Sprintf(`{"path":"f%d.go"}`, i), "ok").
			step("run_tests", `{}`, "ok: 12 passed")
	}
	if f := detectLoops(elements(c.items), DefaultConfig().Loops); f != nil {
		t.Fatalf("new work between repeats is progress, got %+v", f)
	}
}

func TestNoProgressCountsFromTheLastNewThing(t *testing.T) {
	c := newConv().
		step("list_files", `{}`, "a.go").
		step("read_file", `{"path":"a.go"}`, "package a").
		step("grep", `{"q":"TODO"}`, "a.go:3")
	// Now the agent revisits what it has already tried, in no regular order.
	for _, tool := range []string{"list", "grep", "read", "read"} {
		c.revisit(tool)
	}
	if f := detectLoops(elements(c.items), DefaultConfig().Loops); f != nil {
		t.Fatalf("4 revisits is under the threshold of 5, got %+v", f)
	}
	c.revisit("list")
	f := detectLoops(elements(c.items), DefaultConfig().Loops)
	if f == nil || f.Rule != RuleNoProgress || f.Count != 5 {
		t.Fatalf("want no_progress x5 on the fifth revisit, got %+v", f)
	}
	if got := toolList(f.Tools); got != "list_files, grep, read_file" {
		t.Fatalf("tools should be listed once each, oldest first: %q", got)
	}
}

func (c *conv) revisit(tool string) *conv {
	switch tool {
	case "list":
		return c.step("list_files", `{}`, "a.go")
	case "read":
		return c.step("read_file", `{"path":"a.go"}`, "package a")
	}
	return c.step("grep", `{"q":"TODO"}`, "a.go:3")
}

func TestNoProgressResetsWhenAPersonSaysSomethingNew(t *testing.T) {
	c := newConv()
	for i := 1; i <= 7; i++ {
		c.step("check_job", `{"job_id":"42"}`, `{"status":"running"}`).asst(fmt.Sprintf("Check %d.", i)).user("Continue.")
	}
	c.user("The job id is 43, not 42.").step("check_job", `{"job_id":"42"}`, `{"status":"running"}`)
	if f := detectLoops(elements(c.items), DefaultConfig().Loops); f != nil {
		t.Fatalf("a new message from a person starts a new episode, got %+v", f)
	}
}

func TestTheFirstContinueLooksLikeAPersonUntilItRepeats(t *testing.T) {
	els := elements(newConv().step("a", `{}`, "x").user("Continue.").step("a", `{}`, "x").items)
	if got := episodeStart(els); got != 3 {
		t.Fatalf("one \"Continue.\" is a new message: want episode start 3, got %d", got)
	}
	els = elements(newConv().step("a", `{}`, "x").user("Continue.").step("a", `{}`, "x").user("Continue.").step("a", `{}`, "x").items)
	if got := episodeStart(els); got != 1 {
		t.Fatalf("a repeated \"Continue.\" is automated: want episode start 1, got %d", got)
	}
}

func TestNoProgressSeesAFixThatLeavesTheSameFailure(t *testing.T) {
	// What a real model did in the study: edit, run the tests, get the same failure, edit
	// again. Every edit is new and "works", so nothing repeats, but nothing is fixed either.
	cfg := DefaultConfig().Loops
	const fail = "FAIL: TestParse: expected 3 items, got 4 (1 of 12 tests failed)"
	attempt := func(c *conv, i int) *conv {
		return c.step("edit_file", fmt.Sprintf(`{"path":"parser.go","content":"attempt %d"}`, i), "ok")
	}
	c := newConv().
		step("run_tests", `{}`, fail).
		step("read_file", `{"path":"parser.go"}`, "package main")
	for i := 1; i <= 4; i++ {
		attempt(c, i).step("run_tests", `{}`, fail)
	}
	if f := detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("the same failure coming back 4 times is under the threshold of 5, got %+v", f)
	}
	attempt(c, 5).step("run_tests", `{}`, fail)
	f := detectLoops(elements(c.items), cfg)
	if f == nil || f.Rule != RuleNoProgress || f.Count != 5 || toolList(f.Tools) != "run_tests" {
		t.Fatalf("want no_progress x5 naming run_tests, got %+v", f)
	}
	// A sixth edit might be the one that works, so the guard waits for the tests. They fail
	// the same way: the edit did not reset anything.
	attempt(c, 6)
	if f = detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("a new edit is given the benefit of the doubt until the tests run, got %+v", f)
	}
	c.step("run_tests", `{}`, fail)
	if f = detectLoops(elements(c.items), cfg); f == nil || f.Rule != RuleNoProgress || f.Count != 6 || f.Action != ActionNudge {
		t.Fatalf("want no_progress x6: the sixth edit left the failure where it was, got %+v", f)
	}
	for i := 7; i <= 8; i++ {
		attempt(c, i).step("run_tests", `{}`, fail)
	}
	if f = detectLoops(elements(c.items), cfg); f == nil || f.Action != ActionBlock || f.Count != 8 {
		t.Fatalf("want a block when the same failure has come back 8 times, got %+v", f)
	}
	// Now the tests pass: that is new, and it worked.
	attempt(c, 9).step("run_tests", `{}`, "ok: 12 of 12 tests passed")
	if f = detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("a different result is progress, got %+v", f)
	}
}

func TestNoProgressSeesAFixAndItsTestRunInOneTurn(t *testing.T) {
	// Models often write the edit and the test run in the same turn. They still run in order.
	const fail = "FAIL: TestParse: expected 3 items, got 4 (1 of 12 tests failed)"
	c := newConv().step("run_tests", `{}`, fail)
	for i := 1; i <= 5; i++ {
		c.together([3]string{"edit_file", fmt.Sprintf(`{"content":"attempt %d"}`, i), "ok"}, [3]string{"run_tests", `{}`, fail})
	}
	f := detectLoops(elements(c.items), DefaultConfig().Loops)
	if f == nil || f.Rule != RuleNoProgress || f.Count != 5 {
		t.Fatalf("want no_progress x5, got %+v", f)
	}
	// A new call written after the failing run in the same turn has not been judged yet.
	c.together([3]string{"run_tests", `{}`, fail}, [3]string{"read_file", `{"path":"fixture.txt"}`, "a,b,c,"})
	if f = detectLoops(elements(c.items), DefaultConfig().Loops); f != nil {
		t.Fatalf("a new successful call after the failure is progress, got %+v", f)
	}
}

func TestNoProgressLeavesAFailureThatKeepsChangingAlone(t *testing.T) {
	// Real debugging: the suite fails on every run, but with fewer failures each time.
	c := newConv()
	for i := 14; i >= 1; i-- {
		c.step("edit_file", fmt.Sprintf(`{"path":"f%d.go"}`, i), "ok").
			step("run_tests", `{}`, fmt.Sprintf("FAIL: %d of 40 tests failed", i))
	}
	if f := detectLoops(elements(c.items), DefaultConfig().Loops); f != nil {
		t.Fatalf("a failure that changes on every run is progress being made, got %+v", f)
	}
}

func TestStalledRulesCountTurnsNotCalls(t *testing.T) {
	cfg := DefaultConfig().Loops
	// One turn that reads nine files it has read before is one turn with nothing new.
	c := newConv()
	var again [][3]string
	for i := 1; i <= 9; i++ {
		call := [3]string{"read_file", fmt.Sprintf(`{"path":"f%d.go"}`, i), fmt.Sprintf("package f%d", i)}
		c.step(call[0], call[1], call[2])
		again = append(again, call)
	}
	c.together(again...)
	if f := detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("one turn of re-reads counts once, got %+v", f)
	}
	// One turn that guesses nine file names at once is one turn of failures.
	c = newConv()
	var guesses [][3]string
	for i := 1; i <= 9; i++ {
		guesses = append(guesses, [3]string{"read_file", fmt.Sprintf(`{"path":"guess%d.yaml"}`, i), fmt.Sprintf("error: guess%d.yaml: no such file or directory", i)})
	}
	c.together(guesses...)
	if f := detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("nine failures in one turn count once, got %+v", f)
	}
	// A turn in which a new call works is progress, whatever else was in it.
	c = newConv().step("check_job", `{}`, "running")
	for i := 1; i <= 6; i++ {
		c.together([3]string{"check_job", `{}`, "running"}, [3]string{"read_log", fmt.Sprintf(`{"page":%d}`, i), fmt.Sprintf("log page %d", i)})
	}
	if f := detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("a new successful call in the same turn is progress, got %+v", f)
	}
}

func TestRepeatFailureWithDifferentArguments(t *testing.T) {
	// One tool keeps failing on new guesses while other tools keep working, so no_progress
	// has nothing to say.
	cfg := DefaultConfig().Loops
	c := newConv().step("read_file", `{"path":"parser.go"}`, "package main")
	round := func(i int) {
		c.step("list_dir", fmt.Sprintf(`{"path":"dir%d"}`, i), fmt.Sprintf("dir%d/a.go", i)).
			step("read_file", fmt.Sprintf(`{"path":"fixture%d.txt"}`, i), fmt.Sprintf("error: fixture%d.txt: no such file or directory", i))
	}
	for i := 1; i <= 7; i++ {
		round(i)
	}
	if f := detectLoops(elements(c.items), cfg); f != nil {
		t.Fatalf("7 failures is under the threshold of 8, got %+v", f)
	}
	round(8)
	f := detectLoops(elements(c.items), cfg)
	if f == nil || f.Rule != RuleRepeatFailure || f.Count != 8 || f.Action != ActionWarn {
		t.Fatalf("want repeat_failure x8 as a warning, got %+v", f)
	}
	if !strings.Contains(f.NudgeText(""), "read_file has failed in each of the last 8 turns") {
		t.Fatalf("notice should name the tool and count: %q", f.NudgeText(""))
	}
	for i := 9; i <= 30; i++ {
		round(i)
	}
	if f = detectLoops(elements(c.items), cfg); f == nil || f.Action != ActionWarn || f.Count != 30 {
		t.Fatalf("by default repeat_failure only reports, got %+v", f)
	}
	cfg.RepeatFailure.Action = ActionNudge
	cfg.RepeatFailure.EscalateAfter = 6
	if f = detectLoops(elements(c.items), cfg); f == nil || f.Action != ActionBlock || !f.Escalated {
		t.Fatalf("with escalate_after set it blocks, got %+v", f)
	}
}

func TestRepeatFailureResetsWhenTheToolSucceeds(t *testing.T) {
	c := newConv()
	round := func(i int) {
		c.step("list_dir", fmt.Sprintf(`{"path":"dir%d"}`, i), fmt.Sprintf("dir%d/a.go", i)).
			step("read_file", fmt.Sprintf(`{"path":"guess%d.txt"}`, i), "error: no such file or directory")
	}
	for i := 1; i <= 7; i++ {
		round(i)
	}
	c.step("read_file", `{"path":"found.txt"}`, "the fixture")
	for i := 8; i <= 12; i++ {
		round(i)
	}
	if f := detectLoops(elements(c.items), DefaultConfig().Loops); f != nil {
		t.Fatalf("a success in between resets the count, got %+v", f)
	}
}

func TestRepeatFailureIgnoresACallMadeAgain(t *testing.T) {
	// The same call failing differently each time is things moving, not variations failing.
	c := newConv()
	for i := 12; i >= 1; i-- {
		c.step("list_dir", fmt.Sprintf(`{"path":"dir%d"}`, i), fmt.Sprintf("dir%d/a.go", i)).
			step("run_tests", `{}`, fmt.Sprintf("FAIL: %d of 40 tests failed", i))
	}
	if f := detectLoops(elements(c.items), DefaultConfig().Loops); f != nil {
		t.Fatalf("want nothing, got %+v", f)
	}
}

func TestNewRulesCanBeTurnedOffAndRespectIgnoredTools(t *testing.T) {
	c := newConv()
	for i := 1; i <= 9; i++ {
		c.step("check_job", `{"job_id":"42"}`, `{"status":"running"}`).asst(fmt.Sprintf("Check %d.", i)).user("Continue.")
	}
	els := elements(c.items)
	off := DefaultConfig().Loops
	off.NoProgress.Enabled = boolPtr(false)
	if f := detectLoops(els, off); f != nil {
		t.Fatalf("no_progress is off, got %+v", f)
	}
	ignored := DefaultConfig().Loops
	ignored.IgnoreTools = []string{"check_job"}
	if f := detectLoops(els, ignored); f != nil {
		t.Fatalf("check_job is ignored, got %+v", f)
	}
}

func TestLooksLikeFailure(t *testing.T) {
	for _, s := range []string{
		"error: config.yaml: no such file or directory",
		"  Error: connection refused",
		"FAIL: TestParse: expected 3 items, got 4",
		"Traceback (most recent call last):",
		`{"error":"not found"}`,
		`{ "error": {"code": 404} }`,
		`{"errors":["bad request"]}`,
		"bash: foo: command not found",
		"cat: x: No such file or directory",
		"Command exited with exit status 1",
	} {
		if !LooksLikeFailure(s) {
			t.Errorf("should read as a failure: %q", s)
		}
	}
	for _, s := range []string{
		"ok",
		"package main",
		`{"job_id":"42","status":"running"}`,
		`{"error":null,"data":[1,2,3]}`,
		`{"error": false, "ok": true}`,
		`{"errors":[],"rows":4}`,
		`{"error_count":0}`,
		"12 tests passed, 0 failed",
		"README.md\nmain.go",
		"",
		"The error handling in this file is in handle.go",
	} {
		if LooksLikeFailure(s) {
			t.Errorf("should not read as a failure: %q", s)
		}
	}
}

func TestRepeatOutputWithAutomatedContinue(t *testing.T) {
	c := newConv()
	for i := 1; i <= 4; i++ {
		c.asst("I will now start working on the task.").user("continue")
	}
	f := detectLoops(elements(c.items), DefaultConfig().Loops)
	if f == nil || f.Rule != RuleRepeatOutput || f.Count != 4 {
		t.Fatalf("want repeat_output x4, got %+v", f)
	}
}

func TestSingleStepWithAutomatedUserMessageCountsAsRepeatToolCall(t *testing.T) {
	c := newConv()
	for i := 1; i <= 4; i++ {
		c.step("read_file", `{"path":"missing.yaml"}`, "error: no such file").user("continue")
	}
	f := detectLoops(elements(c.items), DefaultConfig().Loops)
	if f == nil || f.Rule != RuleRepeatToolCall || f.Count != 4 {
		t.Fatalf("want repeat_tool_call x4, got %+v", f)
	}
}

func TestParallelToolCallsRepeatingAreACycle(t *testing.T) {
	c := (&conv{}).user("check both services")
	for i := 0; i < 3; i++ {
		// Two calls issued together, results returned after both.
		a, b := fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)
		c.items = append(c.items,
			Item{Kind: KindToolCall, Sig: SigToolCall("ping", `{"host":"api"}`), Name: "ping", CallID: a},
			Item{Kind: KindToolCall, Sig: SigToolCall("ping", `{"host":"db"}`), Name: "ping", CallID: b},
			Item{Kind: KindToolResult, Sig: SigString("timeout"), CallID: a},
			Item{Kind: KindToolResult, Sig: SigString("timeout"), CallID: b},
		)
	}
	f := detectLoops(elements(c.items), DefaultConfig().Loops)
	if f == nil || f.Rule != RuleToolCycle || f.Count != 3 || f.Period != 2 {
		t.Fatalf("want tool_cycle x3 period 2, got %+v", f)
	}
}

func TestToolCallsWithoutIDsPairInOrder(t *testing.T) {
	c := (&conv{}).user("go")
	for i := 0; i < 4; i++ {
		c.items = append(c.items,
			Item{Kind: KindToolCall, Sig: SigToolCall("ls", `{}`), Name: "ls"},
			Item{Kind: KindToolResult, Sig: SigString("empty")},
		)
	}
	f := detectLoops(elements(c.items), DefaultConfig().Loops)
	if f == nil || f.Rule != RuleRepeatToolCall || f.Count != 4 {
		t.Fatalf("want repeat_tool_call x4, got %+v", f)
	}
}

func TestLongResultsAreComparedInFull(t *testing.T) {
	big := strings.Repeat("x", 100_000)
	if SigString(big+"a") == SigString(big+"b") {
		t.Fatal("results differing at the end must have different signatures")
	}
	mid := big[:50_000] + "y" + big[50_001:]
	if SigString(big) == SigString(mid) {
		t.Fatal("results differing in the middle must have different signatures")
	}
}

func TestToolCallSignatureIgnoresFormattingOnly(t *testing.T) {
	same := [][2]string{
		{`{"a":1,"b":"x y"}`, `{ "b" : "x y" , "a" : 1 }`},
		{`{"a":{"k":[1,2,3]},"b":null}`, "{\n  \"b\": null,\n  \"a\": {\"k\": [1, 2, 3]}\n}"},
		{`{}`, ` { } `},
		{`{"q":"a,b"}`, `{ "q": "a,b" }`},
	}
	for _, p := range same {
		if SigToolCall("t", p[0]) != SigToolCall("t", p[1]) {
			t.Errorf("%s and %s should have the same signature", p[0], p[1])
		}
	}
	different := [][2]string{
		{`{"a":1}`, `{"a":2}`},
		{`{"a":1,"b":2}`, `{"a":2,"b":1}`},
		{`{"a":"x y"}`, `{"a":"xy"}`},  // whitespace inside a string is significant
		{`{"a":[1,2]}`, `{"a":[2,1]}`}, // array order is significant
		{`{"a":1}`, `{"a":1,"b":1}`},
		{`{"path":"a.go"}`, `{"path":"b.go"}`},
		{`not json`, `not  json`},
	}
	for _, p := range different {
		if SigToolCall("t", p[0]) == SigToolCall("t", p[1]) {
			t.Errorf("%s and %s should have different signatures", p[0], p[1])
		}
	}
	if SigToolCall("read", `{"a":1}`) == SigToolCall("write", `{"a":1}`) {
		t.Error("different tools must have different signatures")
	}
}

func TestFingerprintSignatureIsStable(t *testing.T) {
	// FNV-1a of "abc": a fixed value, so the session id derived from it survives restarts.
	if got := StableSig("abc"); got != 0xe71fa2190541574b {
		t.Fatalf("StableSig(abc) = %#x", got)
	}
}

// ---- engine ----

func TestEngineNudgesThenBlocks(t *testing.T) {
	e := testEngine(t, nil)
	c := newConv()
	var last Decision
	for i := 1; i <= 7; i++ {
		c.step("read_file", `{"path":"missing.yaml"}`, "error: no such file")
		last = e.Evaluate(req(fmt.Sprintf("r%d", i), c.build()))
		e.Record("s1", Usage{PromptTokens: 1000, CompletionTokens: 50, TotalTokens: 1050, CostUSD: 0.002, OK: true})
		switch {
		case i < 4 && last.Action != ActionAllow:
			t.Fatalf("request %d: want allow, got %+v", i, last)
		case i >= 4 && i < 7 && (last.Action != ActionNudge || last.NudgeText == ""):
			t.Fatalf("request %d: want nudge with text, got %+v", i, last)
		}
	}
	if last.Action != ActionBlock || last.Kind != "loop" || last.Rule != RuleRepeatToolCall {
		t.Fatalf("request 7: want loop block, got %+v", last)
	}
	s := last.Session
	if s.Requests != 6 || s.Blocked != 1 || s.Nudges != 3 || s.State != "blocked" {
		t.Fatalf("unexpected session counters: %+v", s)
	}
	if s.TokensAvoided != 1000 || s.CostAvoided <= 0 {
		t.Fatalf("avoided estimates not recorded: %+v", s)
	}
	if s.Trail != "aaannnb" {
		t.Fatalf("trail = %q, want aaannnb", s.Trail)
	}
}

func TestSecondAttemptOfSameRequestIsNotCountedTwice(t *testing.T) {
	e := testEngine(t, nil)
	c := newConv().step("ls", `{}`, "a b c").build()
	d1 := e.Evaluate(req("same-id", c))
	d2 := e.Evaluate(req("same-id", c)) // fallback attempt
	if d1.Action != d2.Action || d2.Session.Requests != 1 {
		t.Fatalf("fallback attempt was counted again: %+v", d2.Session)
	}
}

func TestOtherSessionsAreUnaffected(t *testing.T) {
	e := testEngine(t, nil)
	c := newConv()
	for i := 1; i <= 8; i++ {
		c.step("read_file", `{"path":"missing.yaml"}`, "error: no such file")
	}
	if d := e.Evaluate(req("a1", c.build())); d.Action != ActionBlock {
		t.Fatalf("looping session should be blocked, got %+v", d)
	}
	healthy := Request{SessionKey: "s2", SessionID: "s2", RequestID: "b1", CanNudge: true,
		Conv: newConv().step("ls", `{}`, "a b c").build()}
	if d := e.Evaluate(healthy); d.Action != ActionAllow {
		t.Fatalf("a second session must not be affected, got %+v", d)
	}
}

func TestRequestLimit(t *testing.T) {
	e := testEngine(t, func(c *Config) { c.Limits.MaxRequests = 3 })
	c := newConv()
	for i := 1; i <= 3; i++ {
		c.step("tool", fmt.Sprintf(`{"i":%d}`, i), "ok")
		if d := e.Evaluate(req(fmt.Sprintf("r%d", i), c.build())); d.Action != ActionAllow {
			t.Fatalf("request %d should pass, got %+v", i, d)
		}
	}
	c.step("tool", `{"i":4}`, "ok")
	d := e.Evaluate(req("r4", c.build()))
	if d.Action != ActionBlock || d.Kind != "limit" || d.Rule != RuleLimitRequests {
		t.Fatalf("want limit block, got %+v", d)
	}
}

func TestCostAndTokenLimits(t *testing.T) {
	e := testEngine(t, func(c *Config) { c.Limits.MaxCostUSD = 0.01; c.Limits.MaxTokens = 1_000_000 })
	c := newConv().step("a", `{}`, "1")
	if d := e.Evaluate(req("r1", c.build())); d.Action != ActionAllow {
		t.Fatalf("want allow, got %+v", d)
	}
	e.Record("s1", Usage{TotalTokens: 5000, CostUSD: 0.02, OK: true})
	c.step("b", `{}`, "2")
	d := e.Evaluate(req("r2", c.build()))
	if d.Action != ActionBlock || d.Rule != RuleLimitCost {
		t.Fatalf("want cost limit block, got %+v", d)
	}

	e2 := testEngine(t, func(c *Config) { c.Limits.MaxTokens = 4000 })
	c2 := newConv().step("a", `{}`, "1")
	e2.Evaluate(req("r1", c2.build()))
	e2.Record("s1", Usage{TotalTokens: 5000, OK: true})
	c2.step("b", `{}`, "2")
	if d := e2.Evaluate(req("r2", c2.build())); d.Action != ActionBlock || d.Rule != RuleLimitTokens {
		t.Fatalf("want token limit block, got %+v", d)
	}
}

func TestDurationLimit(t *testing.T) {
	e := testEngine(t, func(c *Config) { c.Limits.MaxDuration = Duration(time.Hour) })
	clock := time.Unix(1_700_000_000, 0)
	e.now = func() time.Time { return clock }
	c := newConv().step("a", `{}`, "1")
	if d := e.Evaluate(req("r1", c.build())); d.Action != ActionAllow {
		t.Fatalf("want allow, got %+v", d)
	}
	clock = clock.Add(61 * time.Minute)
	c.step("b", `{}`, "2")
	if d := e.Evaluate(req("r2", c.build())); d.Action != ActionBlock || d.Rule != RuleLimitDuration {
		t.Fatalf("want duration limit block, got %+v", d)
	}
}

func TestRepeatRequestCountsOnlyAnsweredResends(t *testing.T) {
	e := testEngine(t, func(c *Config) { c.Loops.RepeatRequest.Threshold = 3 })
	c := newConv().step("ls", `{}`, "a b c").build()

	// A client retrying after provider errors is not looping.
	for i := 1; i <= 6; i++ {
		if d := e.Evaluate(req(fmt.Sprintf("e%d", i), c)); d.Action != ActionAllow {
			t.Fatalf("retry %d after an error should pass, got %+v", i, d)
		}
		e.Record("s1", Usage{OK: false})
	}

	// The same conversation resent after each successful answer is.
	e2 := testEngine(t, func(c *Config) { c.Loops.RepeatRequest.Threshold = 3 })
	var d Decision
	for i := 1; i <= 3; i++ {
		d = e2.Evaluate(req(fmt.Sprintf("r%d", i), c))
		e2.Record("s1", Usage{OK: true})
	}
	if d.Action != ActionBlock || d.Rule != RuleRepeatRequest {
		t.Fatalf("want repeat_request block on the third answered resend, got %+v", d)
	}
	// Still blocked while nothing changes.
	if d := e2.Evaluate(req("r4", c)); d.Action != ActionBlock {
		t.Fatalf("an unchanged resend should stay blocked, got %+v", d)
	}
	// A changed conversation passes again.
	changed := newConv().step("ls", `{}`, "a b c").user("try the other directory").build()
	if d := e2.Evaluate(req("r5", changed)); d.Action != ActionAllow {
		t.Fatalf("a changed conversation should pass, got %+v", d)
	}
}

func TestAFixedPromptSentManyTimesIsNotALoop(t *testing.T) {
	e := testEngine(t, func(c *Config) { c.Loops.RepeatRequest.Threshold = 3 })
	// A classifier or a health probe: the same single prompt, answered every time.
	c := (&conv{}).user("Reply with the word ok.").build()
	for i := 1; i <= 50; i++ {
		if d := e.Evaluate(req(fmt.Sprintf("p%d", i), c)); d.Action != ActionAllow {
			t.Fatalf("request %d: a repeated plain prompt must pass, got %+v", i, d)
		}
		e.Record("s1", Usage{OK: true})
	}
}

func TestDryRunNeverBlocksOrNudges(t *testing.T) {
	e := testEngine(t, func(c *Config) { c.DryRun = true })
	c := newConv()
	for i := 1; i <= 8; i++ {
		c.step("read_file", `{"path":"missing.yaml"}`, "error: no such file")
		d := e.Evaluate(req(fmt.Sprintf("r%d", i), c.build()))
		if d.Action == ActionBlock || d.Action == ActionNudge || d.NudgeText != "" {
			t.Fatalf("dry run must not interfere, got %+v", d)
		}
		if i == 4 && d.Would != ActionNudge {
			t.Fatalf("request 4 should report would=nudge, got %+v", d)
		}
		if i == 8 && d.Would != ActionBlock {
			t.Fatalf("request 8 should report would=block, got %+v", d)
		}
	}
}

func TestNudgeFallsBackToWarnWhenBodyCannotChange(t *testing.T) {
	e := testEngine(t, nil)
	c := newConv()
	for i := 1; i <= 4; i++ {
		c.step("read_file", `{"path":"missing.yaml"}`, "error: no such file")
	}
	r := req("r1", c.build())
	r.CanNudge = false
	if d := e.Evaluate(r); d.Action != ActionWarn || d.NudgeText != "" {
		t.Fatalf("want warn, got %+v", d)
	}
}

func TestUnguardedRequestPasses(t *testing.T) {
	e := testEngine(t, nil)
	if d := e.Evaluate(Request{Conv: newConv().build()}); d.Action != ActionAllow || d.Session != nil {
		t.Fatalf("a request with no session key must pass untracked, got %+v", d)
	}
	if n := len(e.Sessions()); n != 0 {
		t.Fatalf("no session should be tracked, got %d", n)
	}
}

func TestIdleSessionsExpireAndCapEvicts(t *testing.T) {
	e := testEngine(t, func(c *Config) { c.Session.IdleTTL = Duration(time.Minute); c.Session.MaxSessions = 10 })
	clock := time.Unix(1_700_000_000, 0)
	e.now = func() time.Time { return clock }
	for i := 0; i < 10; i++ {
		r := req("x", newConv().build())
		r.SessionKey = fmt.Sprintf("k%d", i)
		e.Evaluate(r)
		clock = clock.Add(time.Second)
	}
	// The eleventh session forces an eviction of the least recently used.
	r := req("x", newConv().build())
	r.SessionKey = "k10"
	e.Evaluate(r)
	if _, ok := e.Session("k0"); ok {
		t.Fatal("the oldest session should have been evicted")
	}
	if _, ok := e.Session("k10"); !ok {
		t.Fatal("the newest session should be tracked")
	}
	clock = clock.Add(2 * time.Minute)
	e.sweep()
	if n := len(e.Sessions()); n != 0 {
		t.Fatalf("idle sessions should expire, %d left", n)
	}
}

func TestResetClearsALimit(t *testing.T) {
	e := testEngine(t, func(c *Config) { c.Limits.MaxRequests = 1 })
	c := newConv().step("a", `{}`, "1")
	e.Evaluate(req("r1", c.build()))
	c.step("b", `{}`, "2")
	if d := e.Evaluate(req("r2", c.build())); d.Action != ActionBlock {
		t.Fatalf("want block, got %+v", d)
	}
	if !e.Reset("s1") {
		t.Fatal("reset should find the session")
	}
	if d := e.Evaluate(req("r3", c.build())); d.Action != ActionAllow {
		t.Fatalf("after reset the session should pass, got %+v", d)
	}
}

// ---- config ----

func TestParseConfigDefaultsAndOverrides(t *testing.T) {
	cfg, err := ParseConfig(nil)
	if err != nil || cfg.Loops.RepeatToolCall.Threshold != 4 {
		t.Fatalf("defaults: %v %+v", err, cfg.Loops.RepeatToolCall)
	}
	raw := map[string]any{
		"dry_run": true,
		"session": map[string]any{"idle_ttl": "30m"},
		"limits":  map[string]any{"max_cost_usd": 5.5, "max_duration": "4h"},
		"loops": map[string]any{
			"repeat_tool_call": map[string]any{"threshold": 3, "action": "block"},
			"tool_cycle":       map[string]any{"enabled": false},
			"ignore_tools":     []any{"wait"},
		},
	}
	cfg, err = ParseConfig(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.DryRun || cfg.Session.IdleTTL.D() != 30*time.Minute || cfg.Limits.MaxCostUSD != 5.5 ||
		cfg.Limits.MaxDuration.D() != 4*time.Hour || cfg.Loops.RepeatToolCall.Threshold != 3 ||
		cfg.Loops.RepeatToolCall.Action != ActionBlock || on(cfg.Loops.ToolCycle.Enabled) ||
		len(cfg.Loops.IgnoreTools) != 1 {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	// Untouched fields keep their defaults.
	if cfg.Loops.RepeatOutput.Threshold != 4 || cfg.Block.LoopStatusCode != 403 || cfg.Session.MaxSessions != 50_000 {
		t.Fatalf("defaults lost: %+v", cfg)
	}
}

func TestParseConfigRejectsBadValues(t *testing.T) {
	bad := []string{
		`{"session":{"fallback":"guess"}}`,
		`{"session":{"idle_ttl":"soon"}}`,
		`{"limits":{"max_requests":-1}}`,
		`{"limits":{"action":"nudge"}}`,
		`{"loops":{"repeat_tool_call":{"threshold":1}}}`,
		`{"loops":{"repeat_tool_call":{"action":"explode"}}}`,
		`{"loops":{"window":20,"tool_cycle":{"max_period":6,"repeats":3}}}`,
		`{"block":{"loop_status_code":500}}`,
		`{"loops":`,
	}
	for _, b := range bad {
		if _, err := ParseConfig(b); err == nil {
			t.Errorf("config %s should be rejected", b)
		}
	}
}

// ---- cost of a decision ----

func BenchmarkEvaluate200ItemConversation(b *testing.B) {
	cfg := DefaultConfig()
	e := New(cfg)
	defer e.Close()
	c := newConv()
	for i := 0; i < 100; i++ {
		c.step("tool", fmt.Sprintf(`{"i":%d}`, i), strings.Repeat("r", 200))
	}
	conv := c.build()
	if len(conv.Items) > cfg.Loops.Window {
		conv.Items = conv.Items[len(conv.Items)-cfg.Loops.Window:]
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Evaluate(Request{SessionKey: "bench", RequestID: fmt.Sprintf("r%d", i), Conv: conv, CanNudge: true})
	}
}
