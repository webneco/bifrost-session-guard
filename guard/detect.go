package guard

import (
	"fmt"
	"strings"
)

// Rule names reported in decisions, headers and the status API.
const (
	RuleRepeatToolCall          = "repeat_tool_call"
	RuleRepeatToolCallAnyResult = "repeat_tool_call_any_result"
	RuleToolCycle               = "tool_cycle"
	RuleRepeatOutput            = "repeat_output"
	RuleNoProgress              = "no_progress"
	RuleRepeatFailure           = "repeat_failure"
	RuleRepeatRequest           = "repeat_request"
	RuleLimitRequests           = "limit_requests"
	RuleLimitTokens             = "limit_tokens"
	RuleLimitCost               = "limit_cost"
	RuleLimitDuration           = "limit_duration"
)

// Finding describes a loop found at the end of a conversation.
type Finding struct {
	Rule string
	// Count is how many times in a row the step, reply or sequence has repeated.
	Count int
	// Threshold is the count at which the rule triggers.
	Threshold int
	// Period is the length of the repeating sequence, in steps and messages.
	Period int
	// Tools lists the tool names in one pass of the repeating sequence, in order.
	Tools []string
	// Action is what the rule asks for, after escalation.
	Action Action
	// Escalated is true when Action was raised to "block" by escalate_after.
	Escalated bool
}

// trailingReps counts how many times the last p elements repeat back to back at the end of
// the sequence. A sequence shorter than p gives 0; a block that appears once gives 1.
func trailingReps(n, p int, sig func(i int) uint64) int {
	if p <= 0 || n < p {
		return 0
	}
	reps := 1
	for {
		start := n - (reps+1)*p
		if start < 0 {
			return reps
		}
		for i := 0; i < p; i++ {
			if sig(start+i) != sig(n-p+i) {
				return reps
			}
		}
		reps++
	}
}

// hasSmallerPeriod reports whether the last p elements are themselves a repetition of a
// shorter block, in which case the shorter period is the one to report.
func hasSmallerPeriod(n, p int, sig func(i int) uint64) bool {
	for q := 1; q < p; q++ {
		if p%q != 0 {
			continue
		}
		same := true
		for i := n - p + q; i < n; i++ {
			if sig(i) != sig(i-q) {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}

func resolve(action Action, threshold, escalateAfter, count int) (Action, bool) {
	if action != ActionBlock && escalateAfter > 0 && count >= threshold+escalateAfter {
		return ActionBlock, true
	}
	return action, false
}

// detectLoops looks for a repeating pattern at the end of the element sequence and returns
// the first rule it triggers, or nil.
func detectLoops(els []element, cfg LoopsConfig) *Finding {
	n := len(els)
	if n < 2 {
		return nil
	}
	isIgnored := func(tool string) bool {
		for _, t := range cfg.IgnoreTools {
			if t == tool {
				return true
			}
		}
		return false
	}
	strict := func(i int) uint64 { return els[i].strict }
	loose := func(i int) uint64 { return els[i].loose }

	// A block with one step may also hold a text element or two, e.g. an automated "continue".
	const maxSingleStepPeriod = 3
	maxP := maxSingleStepPeriod
	if on(cfg.ToolCycle.Enabled) && cfg.ToolCycle.MaxPeriod > maxP {
		maxP = cfg.ToolCycle.MaxPeriod
	}

	describe := func(p int) (steps, texts int, tools []string, allIgnored bool) {
		allIgnored = true
		for _, el := range els[n-p:] {
			switch el.kind {
			case elemStep:
				steps++
				tools = append(tools, el.name)
				if !isIgnored(el.name) {
					allIgnored = false
				}
			case elemAssistantText:
				texts++
			}
		}
		if steps == 0 {
			allIgnored = false
		}
		return
	}

	for p := 1; p <= maxP && p <= n; p++ {
		reps := trailingReps(n, p, strict)
		if reps < 2 || hasSmallerPeriod(n, p, strict) {
			continue
		}
		steps, texts, tools, allIgnored := describe(p)
		if allIgnored {
			continue
		}
		switch {
		case steps == 0:
			if r := cfg.RepeatOutput; on(r.Enabled) && texts > 0 && p <= 2 && reps >= r.Threshold {
				a, esc := resolve(r.Action, r.Threshold, r.EscalateAfter, reps)
				return &Finding{Rule: RuleRepeatOutput, Count: reps, Threshold: r.Threshold, Period: p, Action: a, Escalated: esc}
			}
		case steps == 1:
			if r := cfg.RepeatToolCall; on(r.Enabled) && p <= maxSingleStepPeriod && reps >= r.Threshold {
				a, esc := resolve(r.Action, r.Threshold, r.EscalateAfter, reps)
				return &Finding{Rule: RuleRepeatToolCall, Count: reps, Threshold: r.Threshold, Period: p, Tools: tools, Action: a, Escalated: esc}
			}
		default:
			if r := cfg.ToolCycle; on(r.Enabled) && p <= r.MaxPeriod && reps >= r.Repeats {
				a, esc := resolve(r.Action, r.Repeats, r.EscalateAfter, reps)
				return &Finding{Rule: RuleToolCycle, Count: reps, Threshold: r.Repeats, Period: p, Tools: tools, Action: a, Escalated: esc}
			}
		}
	}

	// Same call, differing results: only for a block with exactly one step.
	if r := cfg.RepeatToolCallAnyResult; on(r.Enabled) {
		for p := 1; p <= maxSingleStepPeriod && p <= n; p++ {
			reps := trailingReps(n, p, loose)
			if reps < r.Threshold || hasSmallerPeriod(n, p, loose) {
				continue
			}
			steps, _, tools, allIgnored := describe(p)
			if steps != 1 || allIgnored {
				continue
			}
			a, esc := resolve(r.Action, r.Threshold, r.EscalateAfter, reps)
			return &Finding{Rule: RuleRepeatToolCallAnyResult, Count: reps, Threshold: r.Threshold, Period: p, Tools: tools, Action: a, Escalated: esc}
		}
	}
	return detectStalled(els, cfg, isIgnored)
}

// episodeStart returns the index just after the last user message that says something new.
//
// A user message that repeats an earlier one word for word is treated as automated: an
// unattended harness answering every attempt to stop with the same "continue". Such messages
// do not start a new episode, so a loop that runs through them is still one loop. A message a
// person actually wrote is new text, and resets everything after it.
func episodeStart(els []element) int {
	for i := len(els) - 1; i >= 0; i-- {
		if els[i].kind != elemUser {
			continue
		}
		repeated := false
		for j := range els {
			if j != i && els[j].kind == elemUser && els[j].strict == els[i].strict {
				repeated = true
				break
			}
		}
		if !repeated {
			return i + 1
		}
	}
	return 0
}

// detectStalled finds loops that do not repeat back to back, which the trailing-repetition
// rules above cannot see: the agent's steps are interleaved with other steps, with its own
// reworded messages, or with a harness telling it to continue.
func detectStalled(els []element, cfg LoopsConfig, isIgnored func(string) bool) *Finding {
	noProgress, repeatFailure := cfg.NoProgress, cfg.RepeatFailure
	if !on(noProgress.Enabled) && !on(repeatFailure.Enabled) {
		return nil
	}
	start := episodeStart(els)
	if r := noProgress; on(r.Enabled) {
		if f := detectNoProgress(els, start, r, isIgnored); f != nil {
			return f
		}
	}
	if r := repeatFailure; on(r.Enabled) {
		return detectRepeatFailure(els, start, r, isIgnored)
	}
	return nil
}

// turnStart returns the index of the first step of the turn that the step at i belongs to.
func turnStart(els []element, start, i int) int {
	lo := i
	for lo-1 >= start && els[lo-1].kind == elemStep && els[lo-1].turn == els[i].turn {
		lo--
	}
	return lo
}

// detectNoProgress counts, from the end of the conversation backwards, the turns in which
// nothing new worked, since the last turn in which something did.
//
// A turn makes progress when at least one of its calls had not been made with that result
// before in this episode, and did not fail. Two kinds of turn are counted as being without
// progress, both read from the structure of the conversation and not from its wording:
//
//   - a turn whose calls returned what the same calls had already returned;
//   - a reply that ended the turn and was answered with a message the caller had sent before,
//     which is what an unattended harness does when it will not take "I am done" or "I am
//     stuck" for an answer.
//
// A new call that failed is neither. It is an attempt, so it does not reset the count, but
// it is not counted: failure is read from free text (see LooksLikeFailure), and in a study
// with real models a notice sent on that evidence made a model give up on a task it could
// have finished. repeat_failure reports those turns to the operator instead.
//
// One more case: a new call that works but leaves a failure exactly as it was is not
// progress. When a call fails with the very result it failed with the last time it was made,
// whatever was done in between did not fix it. That is how an agent that edits a file, runs
// the tests and gets the same failure, over and over, is seen for what it is. A call to an
// ignored tool is expected to repeat and counts as progress.
func detectNoProgress(els []element, start int, r LoopRule, isIgnored func(string) bool) *Finding {
	// Nothing past the point of blocking changes the outcome, so the walk can stop there.
	limit := r.Threshold
	if r.EscalateAfter > 0 {
		limit += r.EscalateAfter
	}
	count, sawStep := 0, false
	var tools []string
	// unchanged is the index of the earliest step whose failure has come back unchanged later
	// in the conversation. A success after it did not fix that failure.
	unchanged := len(els)

	for i := len(els) - 1; i >= start && count < limit; {
		e := &els[i]
		if e.kind == elemAssistantText && i+1 < len(els) && els[i+1].kind == elemUser {
			count++
		}
		if e.kind != elemStep {
			i--
			continue
		}
		sawStep = true
		lo := turnStart(els, start, i)
		progress, repeated := false, false
		kept := len(tools)
		// Calls made in one turn run in the order they were written, so an edit written
		// before a test run in the same turn is judged by that run. Walk the turn backwards.
		for k := i; k >= lo && !progress; k-- {
			s := &els[k]
			if isIgnored(s.name) {
				progress = true
				break
			}
			// prev is the last time the same call was made; seen is whether it has ever
			// returned this result before.
			prev, seen := -1, false
			for j := k - 1; j >= start; j-- {
				if els[j].kind != elemStep {
					continue
				}
				if prev < 0 && els[j].loose == s.loose {
					prev = j
				}
				if els[j].strict == s.strict {
					seen = true
					break
				}
			}
			switch {
			case seen:
				repeated = true
				tools = append(tools, s.name)
				if prev >= 0 && els[prev].strict == s.strict && prev < unchanged && s.failed() {
					unchanged = prev
				}
			case s.failed():
				// A new attempt that failed: not progress, and not counted.
			case k > unchanged:
				// New, and it worked, but the failure it was meant to fix came back unchanged.
			default:
				progress = true
			}
		}
		if progress {
			tools = tools[:kept]
			break
		}
		if repeated {
			count++
		}
		i = lo - 1
	}
	if count < r.Threshold {
		return nil
	}
	if !sawStep {
		// Replies and repeated messages only. With no tool call anywhere in the episode this
		// is a person asking a chat model to "continue" a long answer, not an agent.
		for i := start; i < len(els) && !sawStep; i++ {
			sawStep = els[i].kind == elemStep
		}
		if !sawStep {
			return nil
		}
	}
	// Oldest first, as the other rules list them.
	for i, j := 0, len(tools)-1; i < j; i, j = i+1, j-1 {
		tools[i], tools[j] = tools[j], tools[i]
	}
	a, esc := resolve(r.Action, r.Threshold, r.EscalateAfter, count)
	return &Finding{Rule: RuleNoProgress, Count: count, Threshold: r.Threshold, Period: 1, Tools: tools, Action: a, Escalated: esc}
}

// detectRepeatFailure counts the turns in a row in which the tool that has just failed was
// tried in a new way and failed, whatever other tools ran in between. A turn in which it
// succeeded at least once ends the count.
//
// Only calls that had not been made before count. A call that is made again and fails
// differently (a test suite with fewer failures on every run) shows that things are moving,
// and one that fails the same way again is no_progress's business.
func detectRepeatFailure(els []element, start int, r LoopRule, isIgnored func(string) bool) *Finding {
	last := -1
	for i := len(els) - 1; i >= start; i-- {
		if els[i].kind == elemStep {
			last = i
			break
		}
	}
	if last < 0 || isIgnored(els[last].name) || !els[last].failed() {
		return nil
	}
	name := els[last].name
	firstTime := func(k int) bool {
		for j := k - 1; j >= start; j-- {
			if els[j].kind == elemStep && els[j].loose == els[k].loose {
				return false
			}
		}
		return true
	}
	if !firstTime(last) {
		return nil
	}
	count := 0
	for i := last; i >= start; {
		if els[i].kind != elemStep {
			i--
			continue
		}
		lo := turnStart(els, start, i)
		variation, worked := false, false
		for k := lo; k <= i; k++ {
			if els[k].name != name {
				continue
			}
			if !els[k].failed() {
				worked = true
			} else if !variation {
				variation = firstTime(k)
			}
		}
		if worked {
			break
		}
		if variation {
			count++
		}
		i = lo - 1
	}
	if count < r.Threshold {
		return nil
	}
	a, esc := resolve(r.Action, r.Threshold, r.EscalateAfter, count)
	return &Finding{Rule: RuleRepeatFailure, Count: count, Threshold: r.Threshold, Period: 1, Tools: []string{name}, Action: a, Escalated: esc}
}

func toolList(tools []string) string {
	seen := make(map[string]struct{}, len(tools))
	var out []string
	for _, t := range tools {
		if t == "" {
			t = "a tool"
		}
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return strings.Join(out, ", ")
}

// Reason returns one sentence describing the finding, for logs, errors and the status API.
func (f *Finding) Reason() string {
	switch f.Rule {
	case RuleRepeatToolCall:
		return fmt.Sprintf("the same call to %s returned the same result %d times in a row", toolList(f.Tools), f.Count)
	case RuleRepeatToolCallAnyResult:
		return fmt.Sprintf("the same call to %s was made %d times in a row", toolList(f.Tools), f.Count)
	case RuleToolCycle:
		return fmt.Sprintf("the same sequence of %d tool calls (%s) repeated %d times with the same results", len(f.Tools), toolList(f.Tools), f.Count)
	case RuleRepeatOutput:
		return fmt.Sprintf("the model gave the same reply %d times in a row", f.Count)
	case RuleNoProgress:
		if len(f.Tools) == 0 {
			return fmt.Sprintf("nothing new in the last %d turns: each reply ended the turn and was answered with the same message as before", f.Count)
		}
		return fmt.Sprintf("nothing new in the last %d turns: calls to %s returned what they had returned before, or the turn ended and the same message came back", f.Count, toolList(f.Tools))
	case RuleRepeatFailure:
		return fmt.Sprintf("%s was tried in a new way and failed in %d turns in a row", toolList(f.Tools), f.Count)
	}
	return "a loop was detected"
}

// NudgeText returns the notice added to the request so the model can break the loop itself.
func (f *Finding) NudgeText(suffix string) string {
	var b strings.Builder
	b.WriteString(NudgeMarker)
	b.WriteString(" ")
	switch f.Rule {
	case RuleRepeatToolCall:
		fmt.Fprintf(&b, "Your last %d calls to %s used the same arguments and returned the same result each time. Repeating that call will not change the outcome.", f.Count, toolList(f.Tools))
	case RuleRepeatToolCallAnyResult:
		fmt.Fprintf(&b, "You have made the same call to %s %d times in a row. Check whether it is making progress.", toolList(f.Tools), f.Count)
	case RuleToolCycle:
		fmt.Fprintf(&b, "Your last %d steps repeat the same sequence of tool calls (%s) %d times, with the same results each time. Going round again will not change the outcome.", f.Count*len(f.Tools), toolList(f.Tools), f.Count)
	case RuleRepeatOutput:
		fmt.Fprintf(&b, "Your last %d replies were identical. Saying the same thing again will not help.", f.Count)
	case RuleNoProgress:
		if len(f.Tools) == 0 {
			fmt.Fprintf(&b, "Your last %d replies ended your turn, and each time the same message asked you to continue. Nothing new has happened since.", f.Count)
		} else {
			fmt.Fprintf(&b, "Nothing new has happened in your last %d turns. Calls to %s returned what they had already returned, or you ended your turn and the same message asked you to continue. Do not make those calls again unless something has changed.", f.Count, toolList(f.Tools))
		}
		b.WriteString(" If there is something you have not tried, try it. If not, say what is blocking you in one short reply, and do not start the work again.")
		return withSuffix(&b, suffix)
	case RuleRepeatFailure:
		fmt.Fprintf(&b, "%s has failed in each of the last %d turns that used it, with different arguments each time. If every failure has the same cause, another variation is unlikely to work.", toolList(f.Tools), f.Count)
		b.WriteString(" Try a different approach, or stop and tell the user what is blocking you.")
		return withSuffix(&b, suffix)
	default:
		b.WriteString("You appear to be repeating yourself.")
	}
	b.WriteString(" Do not repeat it. Try a different approach, or stop and tell the user what is blocking you.")
	return withSuffix(&b, suffix)
}

func withSuffix(b *strings.Builder, suffix string) string {
	if s := strings.TrimSpace(suffix); s != "" {
		b.WriteString(" ")
		b.WriteString(s)
	}
	return b.String()
}

// NudgeMarker opens every notice the guard adds to a request. The plugin uses it to avoid
// adding the notice twice to the same request.
const NudgeMarker = "[Bifrost session guard]"
