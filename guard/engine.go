package guard

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// Request is one model request, as the guard sees it.
type Request struct {
	// SessionKey identifies the session inside the engine. Empty means the request is not guarded.
	SessionKey string
	// SessionID is the session id shown to people (headers, status API).
	SessionID string
	// Source says where the session id came from: "header" or "fingerprint".
	Source string
	// VirtualKey is the name or id of the caller's virtual key, when there is one.
	VirtualKey string
	// RequestID identifies the request across provider attempts.
	RequestID string
	// Conv is the conversation the request carries.
	Conv Conversation
	// CanNudge is false when the request body cannot be changed, so "nudge" falls back to "warn".
	CanNudge bool
}

// Decision is the guard's answer for one request.
type Decision struct {
	Action Action `json:"action"`
	// Kind is "loop" or "limit", or empty when the request is allowed.
	Kind string `json:"kind,omitempty"`
	// Rule names the rule that fired, see the Rule constants.
	Rule string `json:"rule,omitempty"`
	// Reason is one sentence for people.
	Reason string `json:"reason,omitempty"`
	// Count and Threshold describe a loop finding.
	Count     int `json:"count,omitempty"`
	Threshold int `json:"threshold,omitempty"`
	// NudgeText is the notice to add to the request when Action is "nudge".
	NudgeText string `json:"-"`
	// Would is the action that dry-run mode held back. Empty outside dry run.
	Would Action `json:"would,omitempty"`
	// Session is the session's state after this decision. Nil when the request is not guarded.
	Session *Snapshot `json:"-"`
}

// Usage is what one completed provider call consumed.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	CostUSD          float64
	// OK is true when the provider returned a response.
	OK bool
}

// Event is one notable decision kept in a session's recent history.
type Event struct {
	At     time.Time `json:"at"`
	Action Action    `json:"action"`
	Rule   string    `json:"rule"`
	Reason string    `json:"reason"`
	Would  Action    `json:"would,omitempty"`
}

// Snapshot is a copy of a session's state, safe to keep and to encode as JSON.
type Snapshot struct {
	Key        string `json:"key"`
	ID         string `json:"id"`
	Source     string `json:"source"`
	VirtualKey string `json:"virtual_key,omitempty"`
	Model      string `json:"model,omitempty"`
	// State is "ok", "warned", "nudged" or "blocked": the outcome of the latest request.
	State            string    `json:"state"`
	FirstSeen        time.Time `json:"first_seen"`
	LastSeen         time.Time `json:"last_seen"`
	Requests         int64     `json:"requests"`
	Blocked          int64     `json:"blocked"`
	Nudges           int64     `json:"nudges"`
	Warnings         int64     `json:"warnings"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	// TokensAvoided estimates the prompt tokens not sent because requests were blocked.
	TokensAvoided int64 `json:"tokens_avoided_estimate"`
	// CostAvoided estimates the spend avoided by blocked requests, from the session's average cost per request.
	CostAvoided float64 `json:"cost_avoided_estimate_usd"`
	// Trail is the outcome of the session's latest requests, oldest first, one letter each:
	// a (allowed), w (warned), n (nudged), b (blocked).
	Trail  string  `json:"trail"`
	Recent []Event `json:"recent,omitempty"`
}

// Totals summarises the engine since start.
type Totals struct {
	Sessions      int     `json:"sessions"`
	Requests      int64   `json:"requests"`
	Blocked       int64   `json:"blocked"`
	Nudges        int64   `json:"nudges"`
	Warnings      int64   `json:"warnings"`
	TotalTokens   int64   `json:"total_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	TokensAvoided int64   `json:"tokens_avoided_estimate"`
	CostAvoided   float64 `json:"cost_avoided_estimate_usd"`
	DryRun        bool    `json:"dry_run"`
}

const (
	recentEvents = 20
	trailLength  = 48
)

type session struct {
	Snapshot
	// Unchanged-conversation tracking for the repeat_request rule.
	lastConvSig   uint64
	lastConvOK    bool
	sameConvCount int
	sameConvSince time.Time
	// The latest request, so a second provider attempt reuses its decision instead of being counted again.
	lastRequestID string
	lastDecision  Decision
	// lastPromptTokens is the prompt size of the latest completed call, used to estimate tokens avoided.
	lastPromptTokens int64
}

// Engine tracks sessions and makes decisions. It is safe for concurrent use.
type Engine struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]*session
	totals   Totals

	now  func() time.Time
	stop chan struct{}
	once sync.Once
}

// New returns an engine for a validated configuration and starts its expiry sweep.
func New(cfg Config) *Engine {
	e := &Engine{
		cfg:      cfg,
		sessions: make(map[string]*session),
		now:      time.Now,
		stop:     make(chan struct{}),
	}
	e.totals.DryRun = cfg.DryRun
	go e.sweepLoop()
	return e
}

// Config returns the engine's configuration.
func (e *Engine) Config() Config { return e.cfg }

// Close stops the expiry sweep.
func (e *Engine) Close() {
	e.once.Do(func() { close(e.stop) })
}

func (e *Engine) sweepLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			e.sweep()
		}
	}
}

// sweep forgets sessions that have been idle longer than the TTL.
func (e *Engine) sweep() {
	e.mu.Lock()
	defer e.mu.Unlock()
	cutoff := e.now().Add(-e.cfg.Session.IdleTTL.D())
	for k, s := range e.sessions {
		if s.LastSeen.Before(cutoff) {
			delete(e.sessions, k)
		}
	}
}

// evictLocked makes room when the session cap is reached by dropping the least recently used tenth.
func (e *Engine) evictLocked() {
	type kv struct {
		k string
		t time.Time
	}
	all := make([]kv, 0, len(e.sessions))
	for k, s := range e.sessions {
		all = append(all, kv{k, s.LastSeen})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].t.Before(all[j].t) })
	drop := len(all)/10 + 1
	for i := 0; i < drop && i < len(all); i++ {
		delete(e.sessions, all[i].k)
	}
}

func (e *Engine) sessionLocked(r Request, now time.Time) *session {
	s, ok := e.sessions[r.SessionKey]
	if ok && now.Sub(s.LastSeen) > e.cfg.Session.IdleTTL.D() {
		delete(e.sessions, r.SessionKey)
		ok = false
	}
	if !ok {
		if len(e.sessions) >= e.cfg.Session.MaxSessions {
			e.evictLocked()
		}
		s = &session{Snapshot: Snapshot{
			Key: r.SessionKey, ID: r.SessionID, Source: r.Source, VirtualKey: r.VirtualKey,
			State: "ok", FirstSeen: now,
		}}
		e.sessions[r.SessionKey] = s
	}
	s.LastSeen = now
	if r.Conv.Model != "" {
		s.Model = r.Conv.Model
	}
	return s
}

func (s *session) snapshot() *Snapshot {
	c := s.Snapshot
	if len(s.Recent) > 0 {
		c.Recent = append([]Event(nil), s.Recent...)
	}
	return &c
}

// checkLimits returns the first session limit that is already used up.
func (e *Engine) checkLimits(s *session, now time.Time) (rule, reason string) {
	l := e.cfg.Limits
	switch {
	case l.MaxRequests > 0 && s.Requests >= l.MaxRequests:
		return RuleLimitRequests, fmt.Sprintf("the session has made %d requests, the limit is %d", s.Requests, l.MaxRequests)
	case l.MaxTokens > 0 && s.TotalTokens >= l.MaxTokens:
		return RuleLimitTokens, fmt.Sprintf("the session has used %d tokens, the limit is %d", s.TotalTokens, l.MaxTokens)
	case l.MaxCostUSD > 0 && s.CostUSD >= l.MaxCostUSD:
		return RuleLimitCost, fmt.Sprintf("the session has spent $%.4f, the limit is $%.2f", s.CostUSD, l.MaxCostUSD)
	case l.MaxDuration.D() > 0 && now.Sub(s.FirstSeen) >= l.MaxDuration.D():
		return RuleLimitDuration, fmt.Sprintf("the session has run for %s, the limit is %s", now.Sub(s.FirstSeen).Round(time.Second), l.MaxDuration.D())
	}
	return "", ""
}

// Evaluate decides what to do with a request. Call it once per provider attempt; a repeat
// call with the same RequestID returns the first decision without counting the request again.
func (e *Engine) Evaluate(r Request) Decision {
	if r.SessionKey == "" {
		return Decision{Action: ActionAllow}
	}
	now := e.now()

	e.mu.Lock()
	defer e.mu.Unlock()

	s := e.sessionLocked(r, now)
	if r.RequestID != "" && r.RequestID == s.lastRequestID {
		d := s.lastDecision
		d.Session = s.snapshot()
		return d
	}

	d := Decision{Action: ActionAllow}

	// 1. Session limits, checked against what the session has already consumed.
	if rule, reason := e.checkLimits(s, now); rule != "" {
		d = Decision{Action: e.cfg.Limits.Action, Kind: "limit", Rule: rule, Reason: reason}
	}

	// 2. Loops visible in the conversation itself. This needs no stored state: the
	// conversation carries its own history, so the same verdict is reached on any replica.
	els := elements(r.Conv.Items)
	if d.Action != ActionBlock {
		if f := detectLoops(els, e.cfg.Loops); f != nil && rank(f.Action) > rank(d.Action) {
			d = Decision{Action: f.Action, Kind: "loop", Rule: f.Rule, Reason: f.Reason(), Count: f.Count, Threshold: f.Threshold}
			if f.Action == ActionNudge {
				d.NudgeText = f.NudgeText(e.cfg.Nudge.Suffix)
			}
		}
	}

	// 3. The same agent conversation sent again after it was already answered. Only
	// conversations with tool activity count: a service that sends one fixed prompt many
	// times is doing its job, not looping.
	agentic := false
	for i := range els {
		if els[i].kind == elemStep {
			agentic = true
			break
		}
	}
	sig := convSig(r.Conv)
	rr := e.cfg.Loops.RepeatRequest
	switch {
	case sig != s.lastConvSig:
		s.sameConvCount, s.sameConvSince = 1, now
	case s.lastConvOK && now.Sub(s.sameConvSince) > rr.Window.D():
		s.sameConvCount, s.sameConvSince = 1, now
	case s.lastConvOK:
		// Only an answered request counts: a client retrying after a provider error is not looping.
		s.sameConvCount++
	}
	s.lastConvSig = sig
	s.lastConvOK = false
	if on(rr.Enabled) && agentic && s.sameConvCount >= rr.Threshold && rank(rr.Action) > rank(d.Action) {
		d = Decision{
			Action: rr.Action, Kind: "loop", Rule: RuleRepeatRequest,
			Reason: fmt.Sprintf("the same unchanged conversation was sent %d times", s.sameConvCount),
			Count:  s.sameConvCount, Threshold: rr.Threshold,
		}
	}

	// A nudge needs a request body the plugin can change.
	if d.Action == ActionNudge && !r.CanNudge {
		d.Action = ActionWarn
		d.NudgeText = ""
	}

	// Dry run: report, never interfere.
	if e.cfg.DryRun && (d.Action == ActionBlock || d.Action == ActionNudge) {
		d.Would = d.Action
		d.Action = ActionWarn
		d.NudgeText = ""
	}

	// Book-keeping.
	switch d.Action {
	case ActionBlock:
		s.State = "blocked"
		s.Blocked++
		e.totals.Blocked++
		s.TokensAvoided += s.lastPromptTokens
		e.totals.TokensAvoided += s.lastPromptTokens
		if s.Requests > 0 {
			avg := s.CostUSD / float64(s.Requests)
			s.CostAvoided += avg
			e.totals.CostAvoided += avg
		}
	case ActionNudge:
		s.State = "nudged"
		s.Nudges++
		e.totals.Nudges++
	case ActionWarn:
		s.State = "warned"
		s.Warnings++
		e.totals.Warnings++
	default:
		s.State = "ok"
	}
	if d.Action != ActionBlock {
		s.Requests++
		e.totals.Requests++
	}
	s.Trail += string(d.Action[0])
	if len(s.Trail) > trailLength {
		s.Trail = s.Trail[len(s.Trail)-trailLength:]
	}
	if d.Action != ActionAllow {
		ev := Event{At: now, Action: d.Action, Rule: d.Rule, Reason: d.Reason, Would: d.Would}
		s.Recent = append(s.Recent, ev)
		if len(s.Recent) > recentEvents {
			s.Recent = s.Recent[len(s.Recent)-recentEvents:]
		}
	}

	s.lastRequestID = r.RequestID
	s.lastDecision = d
	d.Session = s.snapshot()
	return d
}

func rank(a Action) int {
	switch a {
	case ActionBlock:
		return 3
	case ActionNudge:
		return 2
	case ActionWarn:
		return 1
	}
	return 0
}

// Record adds what a completed provider call consumed to its session.
func (e *Engine) Record(sessionKey string, u Usage) {
	if sessionKey == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.sessions[sessionKey]
	if !ok {
		return
	}
	s.PromptTokens += u.PromptTokens
	s.CompletionTokens += u.CompletionTokens
	s.TotalTokens += u.TotalTokens
	s.CostUSD += u.CostUSD
	e.totals.TotalTokens += u.TotalTokens
	e.totals.CostUSD += u.CostUSD
	if u.OK {
		s.lastConvOK = true
		if u.PromptTokens > 0 {
			s.lastPromptTokens = u.PromptTokens
		}
	}
}

// Sessions returns a snapshot of every tracked session, most recently active first.
func (e *Engine) Sessions() []Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Snapshot, 0, len(e.sessions))
	for _, s := range e.sessions {
		out = append(out, *s.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}

// Session returns one session's snapshot.
func (e *Engine) Session(key string) (Snapshot, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.sessions[key]
	if !ok {
		return Snapshot{}, false
	}
	return *s.snapshot(), true
}

// Reset forgets a session, clearing its counters and any limit it had hit.
func (e *Engine) Reset(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.sessions[key]; !ok {
		return false
	}
	delete(e.sessions, key)
	return true
}

// Totals returns counters since the engine started.
func (e *Engine) Totals() Totals {
	e.mu.Lock()
	defer e.mu.Unlock()
	t := e.totals
	t.Sessions = len(e.sessions)
	return t
}
