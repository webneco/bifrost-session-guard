package guard

import "hash/maphash"

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
	// Head is the opening of a KindToolResult item's output, as returned by Head. The guard
	// reads it only when a rule needs to know whether the result was a failure, so that a
	// healthy conversation pays nothing for the check.
	Head string
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

// seed keys the content signatures. They are only ever compared with each other inside one
// process, so a per-process seed is fine and lets us use the runtime's fast hash.
var seed = maphash.MakeSeed()

// SigString returns a signature for a piece of text, for comparing items within a request.
// It is not stable across processes; use StableSig for anything that must be.
func SigString(s string) uint64 {
	return maphash.String(seed, s)
}

const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

func fnvUint64(h uint64, v uint64) uint64 {
	for i := 0; i < 8; i++ {
		h ^= v & 0xff
		h *= fnvPrime64
		v >>= 8
	}
	return h
}

// StableSig returns an FNV-1a signature that is the same in every process and on every
// replica. The conversation fingerprint uses it, because it becomes a session id.
func StableSig(s string) uint64 {
	h := uint64(fnvOffset64)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= fnvPrime64
	}
	return h
}

// Mix combines signatures into one. Order matters. The result is stable when its inputs are.
func Mix(sigs ...uint64) uint64 {
	h := uint64(fnvOffset64)
	for _, s := range sigs {
		h = fnvUint64(h, s)
	}
	return h
}

// SigToolCall returns a signature for a tool call. Two calls get the same signature when
// they name the same tool and their JSON arguments differ only in whitespace or in the
// order of the top-level keys, so reformatting does not hide a repeat.
func SigToolCall(name, arguments string) uint64 {
	return Mix(SigString(name), sigJSONArgs(arguments))
}

func isJSONSpace(c byte) bool { return c == ' ' || c == '\n' || c == '\t' || c == '\r' }

// sigJSONArgs hashes a JSON object in one pass without allocating. Whitespace outside
// strings is skipped at every depth. The top-level members are hashed separately and
// combined with addition, which makes their order irrelevant. Anything that is not a JSON
// object is hashed as trimmed text.
func sigJSONArgs(s string) uint64 {
	i, j := 0, len(s)
	for i < j && isJSONSpace(s[i]) {
		i++
	}
	for j > i && isJSONSpace(s[j-1]) {
		j--
	}
	s = s[i:j]
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return SigString(s)
	}
	var (
		sum     uint64
		members uint64
		h       = uint64(fnvOffset64)
		depth   = 0
		inStr   = false
		esc     = false
		any     = false
	)
	flush := func() {
		if any {
			sum += h
			members++
		}
		h, any = fnvOffset64, false
	}
	for k := 1; k < len(s)-1; k++ {
		c := s[k]
		if inStr {
			h = (h ^ uint64(c)) * fnvPrime64
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case isJSONSpace(c):
			continue
		case c == '"':
			inStr = true
		case c == '{' || c == '[':
			depth++
		case c == '}' || c == ']':
			depth--
		case c == ',' && depth == 0:
			flush()
			continue
		}
		h = (h ^ uint64(c)) * fnvPrime64
		any = true
	}
	if inStr || depth != 0 {
		return SigString(s) // not well-formed: fall back to the text itself
	}
	flush()
	return Mix(sum, members)
}

// How much of a tool result is read to decide whether it is a failure.
const failurePrefixLen = 160

// Words a failed tool's output opens with, and phrases that appear a little way in
// ("cat: x: No such file or directory"). All lower case.
var failureStarts = []string{
	"error", "err:", "[error", "fatal", "failed", "failure", "fail:", "exception", "traceback", "panic:",
	"permission denied", "access denied", "command not found", "not found", "unable to", "cannot ", "could not ",
}

var failureContains = []string{
	"no such file or directory", "non-zero exit", "exit status 1", "exit code 1", "returned non-zero",
	"is not recognized as", "timed out", "connection refused", "command not found", "permission denied",
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

// hasPrefixFold reports whether s starts with the lower-case literal lit, ignoring case.
func hasPrefixFold(s, lit string) bool {
	if len(s) < len(lit) {
		return false
	}
	for i := 0; i < len(lit); i++ {
		if lower(s[i]) != lit[i] {
			return false
		}
	}
	return true
}

// Head returns the part of a tool's output that LooksLikeFailure reads: the first 160 bytes
// after any leading white space. It is a slice of the output, not a copy.
func Head(output string) string {
	i := 0
	for i < len(output) && (output[i] == ' ' || output[i] == '\n' || output[i] == '\t' || output[i] == '\r') {
		i++
	}
	output = output[i:]
	if len(output) > failurePrefixLen {
		output = output[:failurePrefixLen]
	}
	return output
}

// jsonErrorSet reports whether head opens a JSON object whose first key is "error" or
// "errors" with a value that is not null, false, zero or empty.
func jsonErrorSet(head string) bool {
	i := 1
	skip := func() {
		for i < len(head) && (head[i] == ' ' || head[i] == '\n' || head[i] == '\t' || head[i] == '\r') {
			i++
		}
	}
	skip()
	rest := head[i:]
	switch {
	case hasPrefixFold(rest, `"errors"`):
		i += len(`"errors"`)
	case hasPrefixFold(rest, `"error"`):
		i += len(`"error"`)
	default:
		return false
	}
	skip()
	if i >= len(head) || head[i] != ':' {
		return false
	}
	i++
	skip()
	rest = head[i:]
	for _, empty := range []string{"null", "false", `""`, "[]", "{}", "0,", "0}"} {
		if hasPrefixFold(rest, empty) {
			return false
		}
	}
	return rest != ""
}

// LooksLikeFailure reports whether a tool's output reads as a failure: it opens with a word
// such as "error" or "failed", or its first 160 characters contain a well-known failure
// phrase. It is a heuristic over free text, because the OpenAI formats carry no failure flag,
// and it only feeds the repeat_failure rule. It makes one pass and allocates nothing.
func LooksLikeFailure(output string) bool {
	head := Head(output)
	if head == "" {
		return false
	}
	first := lower(head[0])
	if first == '{' {
		// A JSON object with an "error" key first, unless the value is empty:
		// {"error": null, "data": ...} is how some tools report success.
		return jsonErrorSet(head)
	}
	for _, lit := range failureStarts {
		if lit[0] == first && hasPrefixFold(head, lit) {
			return true
		}
	}
	for k := 0; k < len(head); k++ {
		c := lower(head[k])
		// Every phrase in failureContains starts with one of these letters.
		if c != 'n' && c != 'e' && c != 'r' && c != 'i' && c != 't' && c != 'c' && c != 'p' {
			continue
		}
		for _, lit := range failureContains {
			if lit[0] == c && hasPrefixFold(head[k:], lit) {
				return true
			}
		}
	}
	return false
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
	// callID links a step to its result while pairing.
	callID string
	// done is false for a tool call whose result is not in the window.
	done bool
	// head is the opening of the step's result, kept for the failure check.
	head string
	// turn numbers the model turn a step belongs to. Calls the model asked for together, in
	// one reply, share a number.
	turn int32
}

// failed reports whether the step's result reads as a failure.
func (e *element) failed() bool {
	return e.done && LooksLikeFailure(e.head)
}

// elements turns conversation items into the sequence loop detection runs over.
//
// Tool calls are paired with their results. Assistant text that introduces a tool call is
// dropped, because models reword that text ("let me try again") while repeating the same call.
func elements(items []Item) []element {
	out := make([]element, 0, len(items))
	// open holds the indexes of calls still waiting for a result. Results follow their calls
	// closely, so this stays short and a scan beats a map.
	var openBuf [8]int
	open := openBuf[:0]
	// Tool calls with nothing between them were asked for in the same turn.
	turn, inCalls := int32(0), false

	for _, it := range items {
		if it.Kind != KindToolCall && it.Kind != KindOther {
			inCalls = false
		}
		switch it.Kind {
		case KindToolCall:
			if !inCalls {
				turn++
				inCalls = true
			}
			open = append(open, len(out))
			out = append(out, element{kind: elemStep, loose: it.Sig, name: it.Name, callID: it.CallID, turn: turn})
		case KindToolResult:
			at := -1
			if it.CallID != "" {
				for k := len(open) - 1; k >= 0; k-- {
					if out[open[k]].callID == it.CallID {
						at = k
						break
					}
				}
			}
			if at == -1 {
				// No id, or an id we have not seen: pair with the oldest open call that has no id.
				for k := 0; k < len(open); k++ {
					if out[open[k]].callID == "" {
						at = k
						break
					}
				}
			}
			if at >= 0 {
				idx := open[at]
				out[idx].strict = Mix(out[idx].loose, it.Sig)
				out[idx].done = true
				out[idx].head = it.Head
				open = append(open[:at], open[at+1:]...)
			}
		case KindAssistantText:
			sig := Mix(1, it.Sig)
			out = append(out, element{kind: elemAssistantText, strict: sig, loose: sig, done: true})
		case KindUser:
			sig := Mix(2, it.Sig)
			out = append(out, element{kind: elemUser, strict: sig, loose: sig, done: true})
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
	h := fnvUint64(fnvOffset64, SigString(c.Model))
	h = fnvUint64(h, uint64(c.Total))
	h = fnvUint64(h, c.RootSig)
	for i := range c.Items {
		h = fnvUint64(h, uint64(c.Items[i].Kind))
		h = fnvUint64(h, c.Items[i].Sig)
	}
	return h
}
