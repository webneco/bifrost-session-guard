// Package sessionguard is a Bifrost plugin that stops runaway agent sessions.
//
// Budgets and rate limits in Bifrost apply to a virtual key, team, provider or model. When
// one agent gets stuck in a loop it spends that shared allowance, and once the limit trips
// everyone on the key is blocked. The session guard watches each agent session on its own:
// it caps what one session may consume, notices when the conversation is going round in
// circles, and steps in for that session only.
package sessionguard

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/webneco/bifrost-session-guard/guard"
)

// PluginName is the name the plugin registers under.
const PluginName = "session-guard"

// Error types returned to the caller when a request is blocked.
const (
	ErrorTypeLoopDetected  = "session_guard_loop_detected"
	ErrorTypeLimitExceeded = "session_guard_limit_exceeded"
)

// Response headers set on every guarded request.
const (
	HeaderAction   = "x-bf-session-guard"          // allow | warn | nudge | block
	HeaderRule     = "x-bf-session-guard-rule"     // the rule that fired, when one did
	HeaderWould    = "x-bf-session-guard-would"    // what dry-run mode held back
	HeaderSession  = "x-bf-session-guard-session"  // the session id the guard used
	HeaderRequests = "x-bf-session-guard-requests" // requests the session has made
	HeaderCost     = "x-bf-session-guard-cost-usd" // what the session has spent
)

// ctxKeyCarrier holds a *carrier for the life of one request.
//
// The transport hooks and the LLM hooks see different contexts: values set by a transport
// hook are visible to the LLM hooks, but not the other way round. So the transport pre-hook
// puts an empty carrier in the context, the LLM pre-hook fills it in, and the response
// headers hook reads it back.
const ctxKeyCarrier schemas.BifrostContextKey = "session-guard-carrier"

type carrier struct {
	mu       sync.Mutex
	key      string
	decision *guard.Decision
	stream   bool
}

func (c *carrier) set(key string, d *guard.Decision, stream bool) {
	c.mu.Lock()
	c.key, c.decision, c.stream = key, d, stream
	c.mu.Unlock()
}

func (c *carrier) get() (string, *guard.Decision, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.key, c.decision, c.stream
}

func carrierOf(ctx *schemas.BifrostContext) *carrier {
	if ctx == nil {
		return nil
	}
	c, _ := ctx.Value(ctxKeyCarrier).(*carrier)
	return c
}

// Plugin implements Bifrost's LLM plugin hooks and the response-headers transport hook.
type Plugin struct {
	engine *guard.Engine
	status *statusServer
}

// New builds the plugin from the "config" value of its entry in Bifrost's config.json.
func New(config any) (*Plugin, error) {
	cfg, err := guard.ParseConfig(config)
	if err != nil {
		return nil, err
	}
	p := &Plugin{engine: guard.New(cfg)}
	if addr := strings.TrimSpace(cfg.Status.Listen); addr != "" {
		s, err := startStatusServer(addr, p.engine)
		if err != nil {
			p.engine.Close()
			return nil, fmt.Errorf("session guard: status server: %w", err)
		}
		p.status = s
	}
	return p, nil
}

// Engine exposes the decision engine, for embedding and tests.
func (p *Plugin) Engine() *guard.Engine { return p.engine }

// StatusAddr returns the address the status server is listening on, or "" when it is off.
func (p *Plugin) StatusAddr() string {
	if p.status == nil {
		return ""
	}
	return p.status.addr
}

// GetName returns the plugin's name.
func (p *Plugin) GetName() string { return PluginName }

// Cleanup stops the expiry sweep and the status server.
func (p *Plugin) Cleanup() error {
	p.engine.Close()
	if p.status != nil {
		return p.status.close()
	}
	return nil
}

// HTTPTransportPreHook places an empty carrier in the request's context. See ctxKeyCarrier.
func (p *Plugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if ctx != nil {
		ctx.SetValue(ctxKeyCarrier, &carrier{})
	}
	return nil, nil
}

// PreRequestHook is a no-op: the guard does not take part in routing.
func (p *Plugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

// isStreamRequest reports whether a request type streams its response. The plugin depends
// only on Bifrost's schemas package, which keeps the list of packages it must share with
// the gateway short; this mirrors the helper in Bifrost's core package.
func isStreamRequest(t schemas.RequestType) bool {
	return strings.HasSuffix(string(t), "_stream")
}

// isFinalChunk reports whether the current post-hook call is for the last chunk of a stream.
func isFinalChunk(ctx *schemas.BifrostContext) bool {
	done, _ := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool)
	return done
}

func ctxString(ctx *schemas.BifrostContext, key schemas.BifrostContextKey) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(key).(string)
	return s
}

// sessionOf works out which session a request belongs to.
//
// Bifrost already resolves a session id from x-bf-session-id or from the header a coding
// agent sends on its own (Claude Code, Codex CLI, OpenCode). When there is none, the guard
// can fall back to a fingerprint of how the conversation opened, which stays the same for
// the life of a conversation because agents resend their history on every turn.
func (p *Plugin) sessionOf(ctx *schemas.BifrostContext, conv guard.Conversation) (key, id, source, vk string) {
	cfg := p.engine.Config().Session
	id = ctxString(ctx, schemas.BifrostContextKeySessionID)
	source = "header"
	if id == "" {
		if cfg.Fallback != "fingerprint" || conv.RootSig == 0 {
			return "", "", "", ""
		}
		id = "fp-" + strconv.FormatUint(conv.RootSig, 16)
		source = "fingerprint"
	}
	vk = ctxString(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName)
	if vk == "" {
		vk = ctxString(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	}
	key = id
	if cfg.ScopeByVirtualKey == nil || *cfg.ScopeByVirtualKey {
		key = vk + "\x1f" + id
	}
	return key, id, source, vk
}

// PreLLMHook is the decision point. It runs once per provider attempt; the engine counts a
// request once however many attempts it takes.
func (p *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if ctx == nil || req == nil {
		return req, nil, nil
	}
	conv, ok := conversationOf(req, p.engine.Config().Loops.Window)
	if !ok {
		return req, nil, nil
	}
	key, id, source, vk := p.sessionOf(ctx, conv)
	if key == "" {
		return req, nil, nil
	}

	rawBody, _ := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
	d := p.engine.Evaluate(guard.Request{
		SessionKey: key,
		SessionID:  id,
		Source:     source,
		VirtualKey: vk,
		RequestID:  ctxString(ctx, schemas.BifrostContextKeyRequestID),
		Conv:       conv,
		CanNudge:   !rawBody,
	})

	c := carrierOf(ctx)
	if c == nil {
		// No transport hook ran (Bifrost used as a Go SDK): keep the carrier on this context.
		c = &carrier{}
		ctx.SetValue(ctxKeyCarrier, c)
	}
	c.set(key, &d, isStreamRequest(req.RequestType))

	switch d.Action {
	case guard.ActionBlock:
		ctx.Log(schemas.LogLevelWarn, fmt.Sprintf("blocked session %s (%s): %s", id, d.Rule, d.Reason))
		return req, &schemas.LLMPluginShortCircuit{Error: p.blockError(&d, id)}, nil
	case guard.ActionNudge:
		ctx.Log(schemas.LogLevelWarn, fmt.Sprintf("nudged session %s (%s): %s", id, d.Rule, d.Reason))
		injectNotice(req, d.NudgeText)
	case guard.ActionWarn:
		msg := fmt.Sprintf("warning for session %s (%s): %s", id, d.Rule, d.Reason)
		if d.Would != "" {
			msg += fmt.Sprintf(" [dry run: would %s]", d.Would)
		}
		ctx.Log(schemas.LogLevelWarn, msg)
	}
	return req, nil, nil
}

// blockError builds the error a blocked request receives. Fallbacks are turned off: trying
// the same stuck conversation on another provider would only spend more.
func (p *Plugin) blockError(d *guard.Decision, sessionID string) *schemas.BifrostError {
	cfg := p.engine.Config()
	status, typ := cfg.Block.LoopStatusCode, ErrorTypeLoopDetected
	advice := "Change the approach or ask the user how to proceed; sending the same conversation again will be blocked again."
	if d.Kind == "limit" {
		status, typ = cfg.Block.LimitStatusCode, ErrorTypeLimitExceeded
		advice = fmt.Sprintf("Start a new session, or ask the gateway operator to reset this one. It is forgotten after %s without requests.", cfg.Session.IdleTTL.D())
	}
	msg := fmt.Sprintf("Session guard stopped session %s: %s. %s", sessionID, d.Reason, advice)
	noFallback := false
	rule := d.Rule
	return &schemas.BifrostError{
		IsBifrostError: true,
		Type:           &typ,
		StatusCode:     &status,
		AllowFallbacks: &noFallback,
		Error: &schemas.ErrorField{
			Type:    &typ,
			Code:    &rule,
			Message: msg,
		},
	}
}

// injectNotice adds the guard's notice to the request so the model can break the loop
// itself. It goes into the system prompt, which every provider accepts in any position of
// the conversation. The caller's own slices are copied, never changed in place.
func injectNotice(req *schemas.BifrostRequest, notice string) {
	if notice == "" {
		return
	}
	switch {
	case req.ChatRequest != nil:
		in := req.ChatRequest.Input
		out := make([]schemas.ChatMessage, 0, len(in)+1)
		if len(in) > 0 && (in[0].Role == schemas.ChatMessageRoleSystem || in[0].Role == schemas.ChatMessageRoleDeveloper) {
			first := in[0]
			text := chatText(first.Content)
			if strings.Contains(text, notice) {
				return
			}
			joined := text + "\n\n" + notice
			first.Content = &schemas.ChatMessageContent{ContentStr: &joined}
			out = append(out, first)
			out = append(out, in[1:]...)
		} else {
			out = append(out, schemas.ChatMessage{
				Role:    schemas.ChatMessageRoleSystem,
				Content: &schemas.ChatMessageContent{ContentStr: &notice},
			})
			out = append(out, in...)
		}
		req.ChatRequest.Input = out

	case req.ResponsesRequest != nil:
		r := req.ResponsesRequest
		params := schemas.ResponsesParameters{}
		if r.Params != nil {
			params = *r.Params
		}
		joined := notice
		if params.Instructions != nil && *params.Instructions != "" {
			if strings.Contains(*params.Instructions, notice) {
				return
			}
			joined = *params.Instructions + "\n\n" + notice
		}
		params.Instructions = &joined
		r.Params = &params
	}
}

// usageOf reads token usage from whichever response shape is present.
func usageOf(resp *schemas.BifrostResponse) (prompt, completion, total int64, found bool) {
	if resp == nil {
		return 0, 0, 0, false
	}
	switch {
	case resp.ChatResponse != nil && resp.ChatResponse.Usage != nil:
		u := resp.ChatResponse.Usage
		return int64(u.PromptTokens), int64(u.CompletionTokens), int64(u.TotalTokens), true
	case resp.ResponsesResponse != nil && resp.ResponsesResponse.Usage != nil:
		u := resp.ResponsesResponse.Usage
		return int64(u.InputTokens), int64(u.OutputTokens), int64(u.TotalTokens), true
	case resp.ResponsesStreamResponse != nil && resp.ResponsesStreamResponse.Response != nil && resp.ResponsesStreamResponse.Response.Usage != nil:
		u := resp.ResponsesStreamResponse.Response.Usage
		return int64(u.InputTokens), int64(u.OutputTokens), int64(u.TotalTokens), true
	case resp.TextCompletionResponse != nil && resp.TextCompletionResponse.Usage != nil:
		u := resp.TextCompletionResponse.Usage
		return int64(u.PromptTokens), int64(u.CompletionTokens), int64(u.TotalTokens), true
	}
	return 0, 0, 0, false
}

// PostLLMHook adds what the call consumed to its session. For a streamed response it acts
// on the final chunk only, which is the one that carries usage.
func (p *Plugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		return resp, bifrostErr, nil
	}
	c := carrierOf(ctx)
	if c == nil {
		return resp, bifrostErr, nil
	}
	key, d, stream := c.get()
	if key == "" {
		return resp, bifrostErr, nil
	}
	if d != nil && d.Action == guard.ActionBlock {
		return resp, bifrostErr, nil // our own short-circuit: nothing was spent
	}
	if stream && !isFinalChunk(ctx) {
		return resp, bifrostErr, nil
	}

	u := guard.Usage{OK: resp != nil && bifrostErr == nil}
	if pt, ct, tt, found := usageOf(resp); found {
		u.PromptTokens, u.CompletionTokens, u.TotalTokens = pt, ct, tt
		// Must run inside the hook: it reads pricing scopes off the request context.
		u.CostUSD = ctx.CalculateCost(resp)
	} else if resp == nil && bifrostErr != nil && bifrostErr.ExtraFields.BilledUsage != nil {
		// The call failed after the provider had already processed (and billed) tokens.
		b := bifrostErr.ExtraFields.BilledUsage
		u.PromptTokens, u.CompletionTokens, u.TotalTokens = int64(b.PromptTokens), int64(b.CompletionTokens), int64(b.TotalTokens)
	}
	p.engine.Record(key, u)
	return resp, bifrostErr, nil
}

// HTTPTransportResponseHeadersHook reports the decision on the response, for streamed and
// buffered responses alike.
func (p *Plugin) HTTPTransportResponseHeadersHook(ctx *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
	if ctx == nil || resp == nil {
		return nil
	}
	c := carrierOf(ctx)
	if c == nil {
		return nil
	}
	_, d, _ := c.get()
	if d == nil {
		return nil
	}
	resp.SetHeader(HeaderAction, string(d.Action))
	if d.Rule != "" {
		resp.SetHeader(HeaderRule, d.Rule)
	}
	if d.Would != "" {
		resp.SetHeader(HeaderWould, string(d.Would))
	}
	if s := d.Session; s != nil {
		resp.SetHeader(HeaderSession, s.ID)
		resp.SetHeader(HeaderRequests, strconv.FormatInt(s.Requests, 10))
		resp.SetHeader(HeaderCost, strconv.FormatFloat(s.CostUSD, 'f', 6, 64))
	}
	return nil
}
