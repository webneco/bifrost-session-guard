package sessionguard

import (
	"encoding/json"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/webneco/bifrost-session-guard/guard"
)

// rootScan bounds how far into a conversation we look for its opening user message.
const rootScan = 24

// conversationOf reduces a Bifrost request to the provider-neutral conversation the guard
// evaluates. The second result is false for request types that carry no conversation
// (embeddings, images, file operations, ...), which the guard leaves alone.
func conversationOf(req *schemas.BifrostRequest, window int) (guard.Conversation, bool) {
	switch {
	case req == nil:
		return guard.Conversation{}, false
	case req.ChatRequest != nil:
		return chatConversation(req.ChatRequest, window), true
	case req.ResponsesRequest != nil:
		return responsesConversation(req.ResponsesRequest, window), true
	}
	return guard.Conversation{}, false
}

// ---- Chat Completions shape ----

func chatText(c *schemas.ChatMessageContent) string {
	if c == nil {
		return ""
	}
	if c.ContentStr != nil {
		return *c.ContentStr
	}
	if len(c.ContentBlocks) == 1 && c.ContentBlocks[0].Text != nil {
		return *c.ContentBlocks[0].Text
	}
	var b strings.Builder
	for i := range c.ContentBlocks {
		blk := &c.ContentBlocks[i]
		if blk.Text != nil {
			b.WriteString(*blk.Text)
		} else {
			// A non-text block (image, audio, file): its type keeps the signature honest
			// without hashing the payload.
			b.WriteString("[")
			b.WriteString(string(blk.Type))
			b.WriteString("]")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func chatConversation(r *schemas.BifrostChatRequest, window int) guard.Conversation {
	msgs := r.Input
	conv := guard.Conversation{Model: r.Model, Total: len(msgs)}

	// How the conversation opened: the system prompt and the first user message.
	var sysSig, userSig uint64
	for i := 0; i < len(msgs) && i < rootScan; i++ {
		m := &msgs[i]
		switch m.Role {
		case schemas.ChatMessageRoleSystem, schemas.ChatMessageRoleDeveloper:
			if sysSig == 0 {
				sysSig = guard.SigString(chatText(m.Content))
			}
		case schemas.ChatMessageRoleUser:
			userSig = guard.SigString(chatText(m.Content))
		}
		if userSig != 0 {
			break
		}
	}
	if userSig != 0 {
		conv.RootSig = guard.Mix(sysSig, userSig)
	}

	start := 0
	if len(msgs) > window {
		start = len(msgs) - window
	}
	items := make([]guard.Item, 0, len(msgs)-start+8)
	for i := start; i < len(msgs); i++ {
		m := &msgs[i]
		switch m.Role {
		case schemas.ChatMessageRoleSystem, schemas.ChatMessageRoleDeveloper:
			items = append(items, guard.Item{Kind: guard.KindSystem})
		case schemas.ChatMessageRoleUser:
			items = append(items, guard.Item{Kind: guard.KindUser, Sig: guard.SigString(chatText(m.Content))})
		case schemas.ChatMessageRoleTool:
			it := guard.Item{Kind: guard.KindToolResult, Sig: guard.SigString(chatText(m.Content))}
			if m.ChatToolMessage != nil && m.ChatToolMessage.ToolCallID != nil {
				it.CallID = *m.ChatToolMessage.ToolCallID
			}
			items = append(items, it)
		case schemas.ChatMessageRoleAssistant:
			if text := chatText(m.Content); strings.TrimSpace(text) != "" {
				items = append(items, guard.Item{Kind: guard.KindAssistantText, Sig: guard.SigString(text)})
			}
			if m.ChatAssistantMessage != nil {
				for j := range m.ChatAssistantMessage.ToolCalls {
					tc := &m.ChatAssistantMessage.ToolCalls[j]
					it := guard.Item{Kind: guard.KindToolCall}
					if tc.Function.Name != nil {
						it.Name = *tc.Function.Name
					}
					it.Sig = guard.SigToolCall(it.Name, tc.Function.Arguments)
					if tc.ID != nil {
						it.CallID = *tc.ID
					}
					items = append(items, it)
				}
			}
		default:
			items = append(items, guard.Item{Kind: guard.KindOther})
		}
	}
	conv.Items = items
	return conv
}

// ---- Responses shape ----

func responsesText(c *schemas.ResponsesMessageContent) string {
	if c == nil {
		return ""
	}
	if c.ContentStr != nil {
		return *c.ContentStr
	}
	if len(c.ContentBlocks) == 1 && c.ContentBlocks[0].Text != nil {
		return *c.ContentBlocks[0].Text
	}
	var b strings.Builder
	for i := range c.ContentBlocks {
		blk := &c.ContentBlocks[i]
		if blk.Text != nil {
			b.WriteString(*blk.Text)
		} else {
			b.WriteString("[")
			b.WriteString(string(blk.Type))
			b.WriteString("]")
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// responsesItemSig hashes a tool item of a kind we have no typed handling for (shell,
// apply_patch, computer use, ...). Ids and status change between otherwise identical
// calls, so they are left out.
func responsesItemSig(m *schemas.ResponsesMessage) uint64 {
	cp := *m
	cp.ID, cp.Status = nil, nil
	if m.ResponsesToolMessage != nil {
		tm := *m.ResponsesToolMessage
		tm.CallID = nil
		cp.ResponsesToolMessage = &tm
	}
	b, err := json.Marshal(&cp)
	if err != nil {
		return 0
	}
	return guard.SigString(string(b))
}

func responsesConversation(r *schemas.BifrostResponsesRequest, window int) guard.Conversation {
	in := r.Input
	conv := guard.Conversation{Model: r.Model, Total: len(in)}

	var sysSig, userSig uint64
	if r.Params != nil && r.Params.Instructions != nil {
		sysSig = guard.SigString(*r.Params.Instructions)
	}
	for i := 0; i < len(in) && i < rootScan; i++ {
		m := &in[i]
		if m.Role == nil {
			continue
		}
		switch *m.Role {
		case schemas.ResponsesInputMessageRoleSystem, schemas.ResponsesInputMessageRoleDeveloper:
			if sysSig == 0 {
				sysSig = guard.SigString(responsesText(m.Content))
			}
		case schemas.ResponsesInputMessageRoleUser:
			userSig = guard.SigString(responsesText(m.Content))
		}
		if userSig != 0 {
			break
		}
	}
	if userSig != 0 {
		conv.RootSig = guard.Mix(sysSig, userSig)
	}

	start := 0
	if len(in) > window {
		start = len(in) - window
	}
	items := make([]guard.Item, 0, len(in)-start)
	for i := start; i < len(in); i++ {
		m := &in[i]
		typ := ""
		if m.Type != nil {
			typ = string(*m.Type)
		}
		callID := ""
		if m.ResponsesToolMessage != nil && m.ResponsesToolMessage.CallID != nil {
			callID = *m.ResponsesToolMessage.CallID
		}

		switch {
		case typ == string(schemas.ResponsesMessageTypeFunctionCall) && m.ResponsesToolMessage != nil:
			it := guard.Item{Kind: guard.KindToolCall, CallID: callID}
			if m.ResponsesToolMessage.Name != nil {
				it.Name = *m.ResponsesToolMessage.Name
			}
			args := ""
			if m.ResponsesToolMessage.Arguments != nil {
				args = *m.ResponsesToolMessage.Arguments
			}
			it.Sig = guard.SigToolCall(it.Name, args)
			items = append(items, it)

		case strings.HasSuffix(typ, "_output"):
			it := guard.Item{Kind: guard.KindToolResult, CallID: callID}
			if tm := m.ResponsesToolMessage; tm != nil && tm.Output != nil && tm.Output.ResponsesToolCallOutputStr != nil {
				it.Sig = guard.SigString(*tm.Output.ResponsesToolCallOutputStr)
			} else {
				it.Sig = responsesItemSig(m)
			}
			items = append(items, it)

		case strings.HasSuffix(typ, "_call"):
			it := guard.Item{Kind: guard.KindToolCall, CallID: callID, Name: strings.TrimSuffix(typ, "_call")}
			if m.ResponsesToolMessage != nil && m.ResponsesToolMessage.Name != nil && *m.ResponsesToolMessage.Name != "" {
				it.Name = *m.ResponsesToolMessage.Name
			}
			it.Sig = responsesItemSig(m)
			items = append(items, it)

		case typ == string(schemas.ResponsesMessageTypeMessage) || (typ == "" && m.Role != nil):
			role := ""
			if m.Role != nil {
				role = string(*m.Role)
			}
			switch role {
			case string(schemas.ResponsesInputMessageRoleUser):
				items = append(items, guard.Item{Kind: guard.KindUser, Sig: guard.SigString(responsesText(m.Content))})
			case string(schemas.ResponsesInputMessageRoleAssistant):
				if text := responsesText(m.Content); strings.TrimSpace(text) != "" {
					items = append(items, guard.Item{Kind: guard.KindAssistantText, Sig: guard.SigString(text)})
				}
			default:
				items = append(items, guard.Item{Kind: guard.KindSystem})
			}

		default:
			// Reasoning, item references, compaction and the like carry no loop signal.
			items = append(items, guard.Item{Kind: guard.KindOther})
		}
	}
	conv.Items = items
	return conv
}
