package sessionguard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/webneco/bifrost-session-guard/guard"
)

// ---- request builders ----

func sp(s string) *string { return &s }

func chatMsg(role schemas.ChatMessageRole, text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: role, Content: &schemas.ChatMessageContent{ContentStr: sp(text)}}
}

// chatLoop builds a chat request whose history holds n identical tool steps.
func chatLoop(n int, withSystem bool) *schemas.BifrostRequest {
	var in []schemas.ChatMessage
	if withSystem {
		in = append(in, chatMsg(schemas.ChatMessageRoleSystem, "You are a coding agent."))
	}
	in = append(in, chatMsg(schemas.ChatMessageRoleUser, "Read config.yaml and summarise it."))
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("call_%d", i)
		in = append(in,
			schemas.ChatMessage{
				Role:    schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: sp(fmt.Sprintf("Attempt %d", i))},
				ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{{
					ID:       sp(id),
					Function: schemas.ChatAssistantMessageToolCallFunction{Name: sp("read_file"), Arguments: `{"path":"config.yaml"}`},
				}}},
			},
			schemas.ChatMessage{
				Role:            schemas.ChatMessageRoleTool,
				Content:         &schemas.ChatMessageContent{ContentStr: sp("error: no such file")},
				ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: sp(id)},
			},
		)
	}
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4o-mini", Input: in},
	}
}

// responsesLoop builds a Responses request whose input holds n identical function calls.
func responsesLoop(n int, instructions string) *schemas.BifrostRequest {
	msgType := schemas.ResponsesMessageTypeMessage
	callType := schemas.ResponsesMessageTypeFunctionCall
	outType := schemas.ResponsesMessageTypeFunctionCallOutput
	user := schemas.ResponsesInputMessageRoleUser
	in := []schemas.ResponsesMessage{{
		Type: &msgType, Role: &user,
		Content: &schemas.ResponsesMessageContent{ContentStr: sp("Read config.yaml and summarise it.")},
	}}
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("c%d", i)
		in = append(in,
			schemas.ResponsesMessage{Type: &callType, ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: sp(id), Name: sp("read_file"), Arguments: sp(`{"path":"config.yaml"}`),
			}},
			schemas.ResponsesMessage{Type: &outType, ResponsesToolMessage: &schemas.ResponsesToolMessage{
				CallID: sp(id), Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: sp("error: no such file")},
			}},
		)
	}
	r := &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: "gpt-4o-mini", Input: in}
	if instructions != "" {
		r.Params = &schemas.ResponsesParameters{Instructions: sp(instructions)}
	}
	return &schemas.BifrostRequest{RequestType: schemas.ResponsesRequest, ResponsesRequest: r}
}

func newCtx(session, requestID string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	if session != "" {
		ctx.SetValue(schemas.BifrostContextKeySessionID, session)
	}
	ctx.SetValue(schemas.BifrostContextKeyRequestID, requestID)
	return ctx
}

func newPlugin(t *testing.T, config any) *Plugin {
	t.Helper()
	p, err := New(config)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = p.Cleanup() })
	return p
}

// ---- extraction ----

func TestChatExtractionFindsTheLoop(t *testing.T) {
	conv, ok := conversationOf(chatLoop(4, true), 160)
	if !ok || conv.Total != 10 || conv.Model != "gpt-4o-mini" || conv.RootSig == 0 {
		t.Fatalf("unexpected conversation: ok=%v %+v", ok, conv)
	}
	calls, results := 0, 0
	for _, it := range conv.Items {
		switch it.Kind {
		case guard.KindToolCall:
			calls++
			if it.Name != "read_file" || it.CallID == "" {
				t.Fatalf("bad tool call item: %+v", it)
			}
		case guard.KindToolResult:
			results++
		}
	}
	if calls != 4 || results != 4 {
		t.Fatalf("want 4 calls and 4 results, got %d and %d", calls, results)
	}
}

func TestResponsesExtractionFindsTheLoop(t *testing.T) {
	conv, ok := conversationOf(responsesLoop(4, "You are a coding agent."), 160)
	if !ok || conv.Total != 9 || conv.RootSig == 0 {
		t.Fatalf("unexpected conversation: ok=%v %+v", ok, conv)
	}
	p := newPlugin(t, nil)
	d := p.Engine().Evaluate(guard.Request{SessionKey: "k", RequestID: "r", Conv: conv, CanNudge: true})
	if d.Action != guard.ActionNudge || d.Rule != guard.RuleRepeatToolCall {
		t.Fatalf("want a repeat_tool_call nudge, got %+v", d)
	}
}

func TestSameOpeningGivesSameFingerprintAcrossTurns(t *testing.T) {
	a, _ := conversationOf(chatLoop(1, true), 160)
	b, _ := conversationOf(chatLoop(6, true), 160)
	c, _ := conversationOf(chatLoop(6, false), 160)
	if a.RootSig != b.RootSig {
		t.Fatal("the fingerprint must not change as the conversation grows")
	}
	if a.RootSig == c.RootSig {
		t.Fatal("a different system prompt must give a different fingerprint")
	}
}

func TestWindowLimitsHowMuchIsExamined(t *testing.T) {
	conv, _ := conversationOf(chatLoop(200, true), 40)
	if conv.Total != 402 {
		t.Fatalf("total = %d", conv.Total)
	}
	// 40 messages: 20 assistant messages, each with text and one call, and 20 results.
	if len(conv.Items) != 60 {
		t.Fatalf("want 60 items from a 40-message window, got %d", len(conv.Items))
	}
}

func TestRequestsWithoutAConversationAreLeftAlone(t *testing.T) {
	p := newPlugin(t, nil)
	req := &schemas.BifrostRequest{RequestType: schemas.EmbeddingRequest, EmbeddingRequest: &schemas.BifrostEmbeddingRequest{}}
	out, sc, err := p.PreLLMHook(newCtx("s", "r"), req)
	if out != req || sc != nil || err != nil {
		t.Fatalf("an embedding request must pass untouched: %v %v", sc, err)
	}
	if n := len(p.Engine().Sessions()); n != 0 {
		t.Fatalf("no session should be tracked, got %d", n)
	}
}

// ---- hooks ----

func TestPreHookAllowsNudgesThenBlocks(t *testing.T) {
	p := newPlugin(t, nil)
	for n := 1; n <= 7; n++ {
		req := chatLoop(n, true)
		ctx := newCtx("sess-1", fmt.Sprintf("req-%d", n))
		_, sc, err := p.PreLLMHook(ctx, req)
		if err != nil {
			t.Fatalf("turn %d: %v", n, err)
		}
		system := chatText(req.ChatRequest.Input[0].Content)
		switch {
		case n < 4:
			if sc != nil || strings.Contains(system, guard.NudgeMarker) {
				t.Fatalf("turn %d should pass untouched", n)
			}
		case n < 7:
			if sc != nil || !strings.Contains(system, guard.NudgeMarker) || !strings.Contains(system, "You are a coding agent.") {
				t.Fatalf("turn %d should be nudged, system prompt = %q", n, system)
			}
		default:
			if sc == nil || sc.Error == nil {
				t.Fatalf("turn %d should be blocked", n)
			}
			e := sc.Error
			if *e.StatusCode != 403 || *e.Type != ErrorTypeLoopDetected || *e.Error.Code != guard.RuleRepeatToolCall {
				t.Fatalf("unexpected block error: status=%d type=%s code=%s", *e.StatusCode, *e.Type, *e.Error.Code)
			}
			if e.AllowFallbacks == nil || *e.AllowFallbacks {
				t.Fatal("a blocked request must not fall back to another provider")
			}
			if !strings.Contains(e.Error.Message, "read_file") || !strings.Contains(e.Error.Message, "sess-1") {
				t.Fatalf("message should name the tool and the session: %q", e.Error.Message)
			}
		}
	}
}

func TestNudgeDoesNotChangeTheCallersMessages(t *testing.T) {
	p := newPlugin(t, nil)
	req := chatLoop(4, true)
	original := req.ChatRequest.Input
	before, _ := json.Marshal(original)
	if _, sc, _ := p.PreLLMHook(newCtx("s", "r"), req); sc != nil {
		t.Fatal("unexpected block")
	}
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("the caller's slice was modified in place")
	}
	if len(req.ChatRequest.Input) != len(original) {
		t.Fatal("a nudge with an existing system prompt must not add a message")
	}
}

func TestNudgeAddsASystemMessageWhenThereIsNone(t *testing.T) {
	p := newPlugin(t, nil)
	req := chatLoop(4, false)
	n := len(req.ChatRequest.Input)
	p.PreLLMHook(newCtx("s", "r"), req)
	in := req.ChatRequest.Input
	if len(in) != n+1 || in[0].Role != schemas.ChatMessageRoleSystem || !strings.HasPrefix(chatText(in[0].Content), guard.NudgeMarker) {
		t.Fatalf("want a leading system message carrying the notice, got %d messages, first role %q", len(in), in[0].Role)
	}
}

func TestNudgeIsNotAddedTwiceOnASecondAttempt(t *testing.T) {
	p := newPlugin(t, nil)
	req := chatLoop(4, true)
	ctx := newCtx("s", "same-request")
	p.PreLLMHook(ctx, req)
	p.PreLLMHook(ctx, req) // a fallback attempt of the same request
	if c := strings.Count(chatText(req.ChatRequest.Input[0].Content), guard.NudgeMarker); c != 1 {
		t.Fatalf("notice appears %d times", c)
	}
	if s, _ := p.Engine().Session("\x1fs"); s.Requests != 1 || s.Nudges != 1 {
		t.Fatalf("the second attempt was counted again: %+v", s)
	}
}

func TestResponsesNudgeGoesIntoInstructions(t *testing.T) {
	p := newPlugin(t, nil)
	req := responsesLoop(4, "You are a coding agent.")
	p.PreLLMHook(newCtx("s", "r"), req)
	got := *req.ResponsesRequest.Params.Instructions
	if !strings.HasPrefix(got, "You are a coding agent.") || !strings.Contains(got, guard.NudgeMarker) {
		t.Fatalf("instructions = %q", got)
	}

	bare := responsesLoop(4, "")
	p.PreLLMHook(newCtx("s2", "r"), bare)
	if bare.ResponsesRequest.Params == nil || !strings.HasPrefix(*bare.ResponsesRequest.Params.Instructions, guard.NudgeMarker) {
		t.Fatal("a request with no instructions should get them")
	}
}

func TestRawBodyRequestsAreWarnedNotNudged(t *testing.T) {
	p := newPlugin(t, nil)
	req := chatLoop(4, true)
	ctx := newCtx("s", "r")
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	p.PreLLMHook(ctx, req)
	if strings.Contains(chatText(req.ChatRequest.Input[0].Content), guard.NudgeMarker) {
		t.Fatal("a raw-body request cannot be nudged")
	}
	if s, _ := p.Engine().Session("\x1fs"); s.State != "warned" {
		t.Fatalf("state = %q, want warned", s.State)
	}
}

func TestFingerprintFallbackAndOptOut(t *testing.T) {
	p := newPlugin(t, nil)
	p.PreLLMHook(newCtx("", "r1"), chatLoop(1, true))
	p.PreLLMHook(newCtx("", "r2"), chatLoop(2, true))
	ss := p.Engine().Sessions()
	if len(ss) != 1 || ss[0].Source != "fingerprint" || ss[0].Requests != 2 || !strings.HasPrefix(ss[0].ID, "fp-") {
		t.Fatalf("two turns of one conversation should be one fingerprinted session: %+v", ss)
	}

	off := newPlugin(t, map[string]any{"session": map[string]any{"fallback": "none"}})
	off.PreLLMHook(newCtx("", "r1"), chatLoop(8, true))
	if n := len(off.Engine().Sessions()); n != 0 {
		t.Fatalf("fallback none must leave header-less requests unguarded, got %d sessions", n)
	}
}

func TestVirtualKeysKeepSessionsApart(t *testing.T) {
	p := newPlugin(t, nil)
	a := newCtx("shared-id", "r1")
	a.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyName, "team-a")
	b := newCtx("shared-id", "r2")
	b.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyName, "team-b")
	p.PreLLMHook(a, chatLoop(1, true))
	p.PreLLMHook(b, chatLoop(1, true))
	if n := len(p.Engine().Sessions()); n != 2 {
		t.Fatalf("the same session id under two virtual keys should be two sessions, got %d", n)
	}
}

func TestPostHookRecordsUsageAndSkipsBlockedRequests(t *testing.T) {
	p := newPlugin(t, nil)
	ctx := newCtx("s", "r1")
	p.PreLLMHook(ctx, chatLoop(1, true))
	resp := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 900, CompletionTokens: 100, TotalTokens: 1000},
	}}
	if out, _, err := p.PostLLMHook(ctx, resp, nil); out != resp || err != nil {
		t.Fatalf("the response must pass through: %v", err)
	}
	s, _ := p.Engine().Session("\x1fs")
	if s.PromptTokens != 900 || s.CompletionTokens != 100 || s.TotalTokens != 1000 {
		t.Fatalf("usage not recorded: %+v", s)
	}

	// A blocked request spends nothing, so the post-hook must not record anything for it.
	blocked := newCtx("s", "r2")
	if _, sc, _ := p.PreLLMHook(blocked, chatLoop(9, true)); sc == nil {
		t.Fatal("want a block")
	}
	p.PostLLMHook(blocked, nil, &schemas.BifrostError{})
	if s2, _ := p.Engine().Session("\x1fs"); s2.TotalTokens != 1000 {
		t.Fatalf("a blocked request changed the token count: %+v", s2)
	}
}

func TestStreamedResponsesAreRecordedOnceOnTheFinalChunk(t *testing.T) {
	p := newPlugin(t, nil)
	req := chatLoop(1, true)
	req.RequestType = schemas.ChatCompletionStreamRequest
	ctx := newCtx("s", "r1")
	p.PreLLMHook(ctx, req)
	chunk := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 900, CompletionTokens: 100, TotalTokens: 1000},
	}}
	p.PostLLMHook(ctx, chunk, nil) // a middle chunk
	p.PostLLMHook(ctx, chunk, nil)
	if s, _ := p.Engine().Session("\x1fs"); s.TotalTokens != 0 {
		t.Fatalf("middle chunks must not be recorded: %+v", s)
	}
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	p.PostLLMHook(ctx, chunk, nil)
	if s, _ := p.Engine().Session("\x1fs"); s.TotalTokens != 1000 {
		t.Fatalf("the final chunk should be recorded once: %+v", s)
	}
}

func TestResponseHeadersCarryTheDecision(t *testing.T) {
	p := newPlugin(t, map[string]any{"dry_run": true})
	// The transport pre-hook runs on the outer context; the LLM hooks see its values.
	ctx := newCtx("sess-h", "r1")
	if resp, err := p.HTTPTransportPreHook(ctx, nil); resp != nil || err != nil {
		t.Fatalf("the transport pre-hook must not short-circuit: %v %v", resp, err)
	}
	p.PreLLMHook(ctx, chatLoop(8, true))
	meta := &schemas.HTTPResponseMetadata{Headers: map[string]string{}}
	if err := p.HTTPTransportResponseHeadersHook(ctx, nil, meta); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		HeaderAction: "warn", HeaderRule: guard.RuleRepeatToolCall, HeaderWould: "block",
		HeaderSession: "sess-h", HeaderRequests: "1",
	}
	for k, v := range want {
		if got := meta.Header(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}

func TestLimitBlockUsesGovernanceStyleStatus(t *testing.T) {
	p := newPlugin(t, map[string]any{"limits": map[string]any{"max_requests": 1}})
	p.PreLLMHook(newCtx("s", "r1"), chatLoop(1, true))
	_, sc, _ := p.PreLLMHook(newCtx("s", "r2"), chatLoop(2, true))
	if sc == nil || *sc.Error.StatusCode != 402 || *sc.Error.Type != ErrorTypeLimitExceeded || *sc.Error.Error.Code != guard.RuleLimitRequests {
		t.Fatalf("want a 402 limit error, got %+v", sc)
	}
}

func TestBadConfigIsRejected(t *testing.T) {
	if _, err := New(map[string]any{"loops": map[string]any{"repeat_tool_call": map[string]any{"threshold": 1}}}); err == nil {
		t.Fatal("a threshold of 1 should be rejected")
	}
}

// ---- status server ----

func TestStatusServer(t *testing.T) {
	p := newPlugin(t, map[string]any{"status": map[string]any{"listen": "127.0.0.1:0", "token": "s3cret"}})
	p.PreLLMHook(newCtx("sess-status", "r1"), chatLoop(9, true))
	base := "http://" + p.StatusAddr()

	get := func(path, token string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	if code, _ := get("/api/sessions", ""); code != http.StatusUnauthorized {
		t.Fatalf("the API must require the token, got %d", code)
	}
	if code, _ := get("/healthz", ""); code != http.StatusOK {
		t.Fatalf("healthz = %d", code)
	}
	code, body := get("/api/sessions", "s3cret")
	if code != http.StatusOK || !strings.Contains(body, `"id":"sess-status"`) || !strings.Contains(body, `"state":"blocked"`) {
		t.Fatalf("sessions: %d %s", code, body)
	}
	code, body = get("/api/summary", "s3cret")
	if code != http.StatusOK || !strings.Contains(body, `"blocked":1`) || strings.Contains(body, "s3cret") {
		t.Fatalf("summary must report totals and never echo the token: %d %s", code, body)
	}
	if code, body := get("/", ""); code != http.StatusOK || !strings.Contains(strings.ToLower(body), "<html") {
		t.Fatalf("dashboard: %d", code)
	}

	// Reset the session.
	rq, _ := http.NewRequest(http.MethodPost, base+"/api/reset", strings.NewReader(`{"key":"\u001fsess-status"}`))
	rq.Header.Set("Authorization", "Bearer s3cret")
	res, err := http.DefaultClient.Do(rq)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("reset failed: %v %v", err, res)
	}
	res.Body.Close()
	if n := len(p.Engine().Sessions()); n != 0 {
		t.Fatalf("reset should forget the session, %d left", n)
	}
}
