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
		Item{Kind: KindToolResult, Sig: SigString(result), CallID: id},
	)
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
