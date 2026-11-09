package guard

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Kind classifies one item of a conversation.
type Kind uint8

const (
	// KindOther is anything the guard does not reason about (reasoning items, images, ...).
	KindOther Kind = iota
	// KindSystem is a system or developer message.
	KindSystem
	// KindUser is a user message.
	KindUser
	// KindAssistantText is an assistant message that carries text.
	KindAssistantText
	// KindToolCall is one tool call made by the assistant.
	KindToolCall
	// KindToolResult is the output of one tool call.
	KindToolResult
)

// Item is one conversation item, reduced to what loop detection needs.
type Item struct {
	Kind Kind
	// Sig identifies the item's content: the text of a message, the name and
	// arguments of a tool call, or the output of a tool result.
	Sig uint64
	// Name is the tool name on a KindToolCall item.
	Name string
	// CallID links a tool call to its result. It may be empty, in which case calls and
	// results are paired in order.
	CallID string
}

// Conversation is the provider-neutral view of one request that the guard evaluates.
type Conversation struct {
	// Model is the model the request names.
	Model string
	// Items is the trailing window of the conversation, oldest first.
	Items []Item
	// Total is the number of items in the whole request, not only the window.
	Total int
	// RootSig identifies how the conversation opened (system prompt and first user message).
	// It is stable for the life of a conversation, so it can stand in for a session id.
	RootSig uint64
}

// sigHeadBytes and sigTailBytes bound how much of a long string is hashed. Tool results can be
// megabytes; hashing the length plus the head and tail keeps the cost per item flat.
const (
	sigHeadBytes = 4096
	sigTailBytes = 1024
)

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

func fnvString(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	return h
}

func fnvUint64(h uint64, v uint64) uint64 {
	for i := 0; i < 8; i++ {
		h ^= v & 0xff
		h *= fnvPrime64
		v >>= 8
	}
	return h
}

// SigString returns a signature (FNV-1a) for a piece of text. Long text is sampled, see sigHeadBytes.
func SigString(s string) uint64 {
	if len(s) <= sigHeadBytes+sigTailBytes {
		return fnvString(fnvOffset64, s)
	}
	h := fnvUint64(fnvOffset64, uint64(len(s)))
	h = fnvString(h, s[:sigHeadBytes])
	return fnvString(h, s[len(s)-sigTailBytes:])
}

// Mix combines signatures into one. Order matters.
func Mix(sigs ...uint64) uint64 {
	h := uint64(fnvOffset64)
	for _, s := range sigs {
		h = fnvUint64(h, s)
	}
	return h
}

// SigToolCall returns a signature for a tool call. Arguments that are valid JSON are
// canonicalised first, so key order and whitespace do not hide a repeat.
func SigToolCall(name, arguments string) uint64 {
	return Mix(SigString(name), SigString(CanonicalJSON(arguments)))
}

// CanonicalJSON re-encodes a JSON document with sorted keys and no insignificant whitespace.
// Text that is not valid JSON is returned trimmed.
func CanonicalJSON(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	if t[0] != '{' && t[0] != '[' {
		return t
	}
	dec := json.NewDecoder(strings.NewReader(t))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return t
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return t
	}
	return strings.TrimRight(buf.String(), "\n")
}

type elemKind uint8

const (
	elemStep elemKind = iota
	elemAssistantText
	elemUser
)

// element is one unit of the sequence that loop detection runs over: an agent step (a tool
// call with its result), a text-only assistant reply, or a user message.
type element struct {
	kind elemKind
	// strict identifies the element exactly: for a step, the call and its result.
	strict uint64
	// loose identifies a step by its call alone, whatever it returned.
	loose uint64
	name  string
	// done is false for a tool call whose result is not in the window.
	done bool
}

// elements turns conversation items into the sequence loop detection runs over.
//
// Tool calls are paired with their results. Assistant text that introduces a tool call is
// dropped, because models reword that text ("let me try again") while repeating the same call.
func elements(items []Item) []element {
	out := make([]element, 0, len(items))
	pendingByID := make(map[string]int)
	var pendingFIFO []int

	for _, it := range items {
		switch it.Kind {
		case KindToolCall:
			idx := len(out)
			out = append(out, element{kind: elemStep, loose: it.Sig, name: it.Name})
			if it.CallID != "" {
				pendingByID[it.CallID] = idx
			} else {
				pendingFIFO = append(pendingFIFO, idx)
			}
		case KindToolResult:
			idx := -1
			if it.CallID != "" {
				if i, ok := pendingByID[it.CallID]; ok {
					idx = i
					delete(pendingByID, it.CallID)
				}
			}
			if idx == -1 && len(pendingFIFO) > 0 {
				idx = pendingFIFO[0]
				pendingFIFO = pendingFIFO[1:]
			}
			if idx >= 0 {
				out[idx].strict = Mix(out[idx].loose, it.Sig)
				out[idx].done = true
			}
		case KindAssistantText:
			out = append(out, element{kind: elemAssistantText, strict: Mix(1, it.Sig), loose: Mix(1, it.Sig), done: true})
		case KindUser:
			out = append(out, element{kind: elemUser, strict: Mix(2, it.Sig), loose: Mix(2, it.Sig), done: true})
		}
	}

	// Drop assistant text that leads straight into a tool call, and steps with no result yet.
	kept := out[:0]
	for i, el := range out {
		if el.kind == elemAssistantText && i+1 < len(out) && out[i+1].kind == elemStep {
			continue
		}
		if el.kind == elemStep && !el.done {
			continue
		}
		kept = append(kept, el)
	}
	return kept
}

// convSig identifies the request's conversation as a whole, for spotting an unchanged resend.
func convSig(c Conversation) uint64 {
	h := make([]uint64, 0, len(c.Items)+3)
	h = append(h, SigString(c.Model), uint64(c.Total), c.RootSig)
	for _, it := range c.Items {
		h = append(h, uint64(it.Kind), it.Sig)
	}
	return Mix(h...)
}
