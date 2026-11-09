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
	ignored := make(map[string]struct{}, len(cfg.IgnoreTools))
	for _, t := range cfg.IgnoreTools {
		ignored[t] = struct{}{}
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
				if _, ok := ignored[el.name]; !ok {
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
	return nil
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
	default:
		b.WriteString("You appear to be repeating yourself.")
	}
	b.WriteString(" Do not repeat it. Try a different approach, or stop and tell the user what is blocking you.")
	if s := strings.TrimSpace(suffix); s != "" {
		b.WriteString(" ")
		b.WriteString(s)
	}
	return b.String()
}

// NudgeMarker opens every notice the guard adds to a request. The plugin uses it to avoid
// adding the notice twice to the same request.
const NudgeMarker = "[Bifrost session guard]"
