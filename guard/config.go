// Package guard is the session guard's decision engine. It has no dependency on Bifrost:
// it takes a provider-neutral view of a conversation plus per-session counters and decides
// whether a request should pass, carry a warning, be nudged, or be blocked.
package guard

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Action is what the guard does about a finding.
type Action string

const (
	// ActionAllow lets the request through untouched.
	ActionAllow Action = "allow"
	// ActionWarn lets the request through and reports the finding in headers and logs.
	ActionWarn Action = "warn"
	// ActionNudge lets the request through after adding a notice that tells the model it is looping.
	ActionNudge Action = "nudge"
	// ActionBlock rejects the request before it reaches a provider.
	ActionBlock Action = "block"
)

func (a Action) valid() bool {
	switch a {
	case ActionWarn, ActionNudge, ActionBlock:
		return true
	}
	return false
}

// Duration is a time.Duration that reads from JSON as a string such as "90s" or "2h".
type Duration time.Duration

// UnmarshalJSON accepts a duration string ("2h", "90s") or a number of seconds.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		v, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(v)
		return nil
	}
	var n float64
	if err := json.Unmarshal(b, &n); err != nil {
		return errors.New("duration must be a string like \"2h\" or a number of seconds")
	}
	*d = Duration(time.Duration(n * float64(time.Second)))
	return nil
}

// MarshalJSON writes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// Config is the plugin's configuration. The zero value is not usable; start from DefaultConfig.
type Config struct {
	// DryRun reports what the guard would do (headers, logs, status) without blocking or nudging.
	DryRun  bool          `json:"dry_run"`
	Session SessionConfig `json:"session"`
	Limits  LimitsConfig  `json:"limits"`
	Loops   LoopsConfig   `json:"loops"`
	Block   BlockConfig   `json:"block"`
	Nudge   NudgeConfig   `json:"nudge"`
	Status  StatusConfig  `json:"status"`
}

// SessionConfig controls how requests are grouped into sessions and how long a session is remembered.
type SessionConfig struct {
	// IdleTTL is how long a session is remembered after its last request.
	IdleTTL Duration `json:"idle_ttl"`
	// MaxSessions caps tracked sessions; the least recently used is dropped first.
	MaxSessions int `json:"max_sessions"`
	// Fallback decides what happens when a request carries no session id:
	// "fingerprint" derives one from the conversation's opening messages, "none" leaves the request unguarded.
	Fallback string `json:"fallback"`
	// ScopeByVirtualKey keeps the same session id under two virtual keys apart.
	ScopeByVirtualKey *bool `json:"scope_by_virtual_key"`
}

// LimitsConfig caps what one session may consume. A zero value turns that cap off.
type LimitsConfig struct {
	MaxRequests int64    `json:"max_requests"`
	MaxTokens   int64    `json:"max_tokens"`
	MaxCostUSD  float64  `json:"max_cost_usd"`
	MaxDuration Duration `json:"max_duration"`
	// Action is "block" (default) or "warn".
	Action Action `json:"action"`
}

// LoopsConfig controls loop detection.
type LoopsConfig struct {
	// Window is how many trailing conversation items (messages, tool calls, tool results) are examined.
	Window int `json:"window"`
	// RepeatToolCall fires when the same tool call returns the same result several times in a row.
	RepeatToolCall LoopRule `json:"repeat_tool_call"`
	// RepeatToolCallAnyResult fires when the same tool call repeats whatever it returns.
	// It exists for results that differ only by noise (timestamps, request ids).
	RepeatToolCallAnyResult LoopRule `json:"repeat_tool_call_any_result"`
	// ToolCycle fires when a sequence of two or more steps repeats, such as A, B, A, B, A, B.
	ToolCycle CycleRule `json:"tool_cycle"`
	// RepeatOutput fires when the model gives the same text reply several times in a row.
	RepeatOutput LoopRule `json:"repeat_output"`
	// RepeatRequest fires when a client resends an unchanged conversation after getting an answer.
	RepeatRequest RepeatRequestRule `json:"repeat_request"`
	// IgnoreTools lists tool names that may repeat freely, such as polling or wait tools.
	IgnoreTools []string `json:"ignore_tools"`
}

// LoopRule configures a detector that counts consecutive repeats.
type LoopRule struct {
	Enabled *bool `json:"enabled"`
	// Threshold is the number of consecutive repeats that triggers the rule.
	Threshold int `json:"threshold"`
	// Action is taken when the threshold is reached.
	Action Action `json:"action"`
	// EscalateAfter blocks the session once the repeats reach Threshold + EscalateAfter.
	// Zero means never escalate. It only matters when Action is not already "block".
	EscalateAfter int `json:"escalate_after"`
}

// CycleRule configures detection of a repeating sequence of steps.
type CycleRule struct {
	Enabled *bool `json:"enabled"`
	// MaxPeriod is the longest sequence considered, in steps.
	MaxPeriod int `json:"max_period"`
	// Repeats is how many times the sequence must repeat back to back.
	Repeats       int    `json:"repeats"`
	Action        Action `json:"action"`
	EscalateAfter int    `json:"escalate_after"`
}

// RepeatRequestRule configures detection of an unchanged conversation being resent.
type RepeatRequestRule struct {
	Enabled   *bool    `json:"enabled"`
	Threshold int      `json:"threshold"`
	Window    Duration `json:"window"`
	Action    Action   `json:"action"`
}

// BlockConfig shapes the error a blocked request receives.
type BlockConfig struct {
	// LoopStatusCode is the HTTP status for a loop block. The default, 403, is one that client
	// libraries do not retry, so the agent stops instead of hammering the gateway.
	LoopStatusCode int `json:"loop_status_code"`
	// LimitStatusCode is the HTTP status for an exceeded session limit. The default, 402, matches
	// the status Bifrost governance uses for an exceeded budget.
	LimitStatusCode int `json:"limit_status_code"`
}

// NudgeConfig shapes the notice added to a request when a rule's action is "nudge".
type NudgeConfig struct {
	// Suffix is appended to the built-in notice, for house instructions such as
	// "Ask the user before retrying.".
	Suffix string `json:"suffix"`
}

// StatusConfig turns on the status API and dashboard.
type StatusConfig struct {
	// Listen is an address such as "127.0.0.1:9797". Empty leaves the status server off.
	Listen string `json:"listen"`
}

// itemsPerStep is the number of conversation items one agent step usually takes: optional
// assistant text, the tool call and its result. It sizes the window check in Validate.
const itemsPerStep = 3

func boolPtr(b bool) *bool { return &b }

func on(p *bool) bool { return p == nil || *p }

// DefaultConfig returns the configuration used when a field is left out.
func DefaultConfig() Config {
	return Config{
		Session: SessionConfig{
			IdleTTL:           Duration(2 * time.Hour),
			MaxSessions:       50_000,
			Fallback:          "fingerprint",
			ScopeByVirtualKey: boolPtr(true),
		},
		Limits: LimitsConfig{Action: ActionBlock},
		Loops: LoopsConfig{
			Window:                  160,
			RepeatToolCall:          LoopRule{Enabled: boolPtr(true), Threshold: 4, Action: ActionNudge, EscalateAfter: 3},
			RepeatToolCallAnyResult: LoopRule{Enabled: boolPtr(true), Threshold: 10, Action: ActionNudge, EscalateAfter: 0},
			ToolCycle:               CycleRule{Enabled: boolPtr(true), MaxPeriod: 6, Repeats: 3, Action: ActionNudge, EscalateAfter: 2},
			RepeatOutput:            LoopRule{Enabled: boolPtr(true), Threshold: 4, Action: ActionNudge, EscalateAfter: 3},
			RepeatRequest:           RepeatRequestRule{Enabled: boolPtr(true), Threshold: 8, Window: Duration(2 * time.Minute), Action: ActionBlock},
		},
		Block: BlockConfig{LoopStatusCode: 403, LimitStatusCode: 402},
	}
}

// ParseConfig builds a Config from the plugin's "config" value in Bifrost's config.json.
// It accepts the decoded value (a map), raw JSON bytes, a JSON string, or nil for defaults.
// Fields that are left out keep their defaults.
func ParseConfig(raw any) (Config, error) {
	cfg := DefaultConfig()
	var data []byte
	switch v := raw.(type) {
	case nil:
		return cfg, nil
	case []byte:
		data = v
	case json.RawMessage:
		data = v
	case string:
		data = []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return cfg, fmt.Errorf("session guard: config is not JSON-serialisable: %w", err)
		}
		data = b
	}
	if len(data) == 0 || string(data) == "null" {
		return cfg, nil
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("session guard: invalid config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate reports the first problem in the configuration.
func (c *Config) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("session guard: "+format, a...)
	}
	if c.Session.IdleTTL.D() <= 0 {
		return bad("session.idle_ttl must be positive")
	}
	if c.Session.MaxSessions <= 0 {
		return bad("session.max_sessions must be positive")
	}
	if c.Session.Fallback != "fingerprint" && c.Session.Fallback != "none" {
		return bad("session.fallback must be \"fingerprint\" or \"none\", got %q", c.Session.Fallback)
	}
	if c.Limits.MaxRequests < 0 || c.Limits.MaxTokens < 0 || c.Limits.MaxCostUSD < 0 || c.Limits.MaxDuration.D() < 0 {
		return bad("limits must not be negative")
	}
	if c.Limits.Action != ActionBlock && c.Limits.Action != ActionWarn {
		return bad("limits.action must be \"block\" or \"warn\", got %q", c.Limits.Action)
	}
	if c.Loops.Window < 12 {
		return bad("loops.window must be at least 12")
	}
	rules := []struct {
		name string
		r    LoopRule
	}{
		{"repeat_tool_call", c.Loops.RepeatToolCall},
		{"repeat_tool_call_any_result", c.Loops.RepeatToolCallAnyResult},
		{"repeat_output", c.Loops.RepeatOutput},
	}
	for _, x := range rules {
		if !on(x.r.Enabled) {
			continue
		}
		if x.r.Threshold < 2 {
			return bad("loops.%s.threshold must be at least 2", x.name)
		}
		if !x.r.Action.valid() {
			return bad("loops.%s.action must be \"warn\", \"nudge\" or \"block\", got %q", x.name, x.r.Action)
		}
		if x.r.EscalateAfter < 0 {
			return bad("loops.%s.escalate_after must not be negative", x.name)
		}
		if need := itemsPerStep * (x.r.Threshold + x.r.EscalateAfter); need > c.Loops.Window {
			return bad("loops.window (%d) is too small for %s: need at least %d", c.Loops.Window, x.name, need)
		}
	}
	if cy := c.Loops.ToolCycle; on(cy.Enabled) {
		if cy.MaxPeriod < 2 {
			return bad("loops.tool_cycle.max_period must be at least 2")
		}
		if cy.Repeats < 2 {
			return bad("loops.tool_cycle.repeats must be at least 2")
		}
		if !cy.Action.valid() {
			return bad("loops.tool_cycle.action must be \"warn\", \"nudge\" or \"block\", got %q", cy.Action)
		}
		if cy.EscalateAfter < 0 {
			return bad("loops.tool_cycle.escalate_after must not be negative")
		}
		if need := itemsPerStep * cy.MaxPeriod * (cy.Repeats + cy.EscalateAfter); need > c.Loops.Window {
			return bad("loops.window (%d) is too small for tool_cycle: need at least %d", c.Loops.Window, need)
		}
	}
	if rr := c.Loops.RepeatRequest; on(rr.Enabled) {
		if rr.Threshold < 2 {
			return bad("loops.repeat_request.threshold must be at least 2")
		}
		if rr.Window.D() <= 0 {
			return bad("loops.repeat_request.window must be positive")
		}
		if rr.Action != ActionBlock && rr.Action != ActionWarn {
			return bad("loops.repeat_request.action must be \"block\" or \"warn\", got %q", rr.Action)
		}
	}
	for _, code := range []int{c.Block.LoopStatusCode, c.Block.LimitStatusCode} {
		if code < 400 || code > 499 {
			return bad("block status codes must be 4xx, got %d", code)
		}
	}
	return nil
}
