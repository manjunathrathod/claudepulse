// Package jsonl streams Claude Code session transcripts (<session>.jsonl).
//
// Only the envelope fields the monitor needs are decoded; everything else is
// ignored so that new line types or fields never break indexing. Lines can be
// hundreds of KB, so the reader uses bufio.Reader.ReadBytes rather than a
// bufio.Scanner with a fixed cap.
//
// Important: a single assistant API response is written as several lines
// (one per streamed content block) that share message.id and carry the same
// usage object. Callers must dedupe usage by Message.ID while still collecting
// tool_use blocks from every line. See .claude/skills/claude-dir-format.
package jsonl

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// Line is the decoded envelope of one transcript line.
type Line struct {
	Type        string   `json:"type"`
	UUID        string   `json:"uuid"`
	ParentUUID  string   `json:"parentUuid"`
	SessionID   string   `json:"sessionId"`
	AgentID     string   `json:"agentId"` // set on subagent transcripts
	CWD         string   `json:"cwd"`
	Version     string   `json:"version"`
	Entrypoint  string   `json:"entrypoint"`
	IsSidechain bool     `json:"isSidechain"`
	Timestamp   Time     `json:"timestamp"`
	Message     *Message `json:"message"`
	AITitle     string   `json:"aiTitle"` // type == "ai-title"
	Summary     string   `json:"summary"` // type == "summary"
}

// Time is a lenient timestamp: RFC3339 string, ms-epoch number, or absent.
// Other line types carry differently-typed timestamp fields; a bad value must
// not make the whole line malformed.
type Time struct{ time.Time }

// UnmarshalJSON accepts an RFC3339 string or a millisecond epoch number.
func (t *Time) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return nil
		}
		if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
			t.Time = ts.UTC()
		}
		return nil
	}
	var ms int64
	if err := json.Unmarshal(b, &ms); err == nil && ms > 0 {
		t.Time = time.UnixMilli(ms).UTC()
	}
	return nil
}

// Message is the API message inside user/assistant lines.
type Message struct {
	ID      string  `json:"id"`    // msg_… on assistant lines; empty on user lines
	Role    string  `json:"role"`  // user | assistant
	Model   string  `json:"model"` // assistant only
	Content Content `json:"content"`
	Usage   *Usage  `json:"usage"`
}

// Usage is the token accounting attached to assistant messages.
type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	OutputTokensDetails      struct {
		ThinkingTokens int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

// Block is one content block. Only the discriminating fields are kept.
type Block struct {
	Type      string `json:"type"` // text | thinking | tool_use | tool_result | …
	ID        string `json:"id"`   // tool_use id (toolu_…)
	Name      string `json:"name"` // tool name for tool_use
	ToolUseID string `json:"tool_use_id"`
}

// Content is either a plain string (typed prompt) or an array of blocks.
type Content struct {
	Text   string  // set when the JSON value was a string
	IsText bool    // true when the JSON value was a string
	Blocks []Block // set when the JSON value was an array
}

// UnmarshalJSON accepts both encodings.
func (c *Content) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	if b[0] == '"' {
		c.IsText = true
		return json.Unmarshal(b, &c.Text)
	}
	if b[0] == '[' {
		return json.Unmarshal(b, &c.Blocks)
	}
	// Unknown shape: tolerate.
	return nil
}

// Kind classifies a message's content for counting purposes.
func (c Content) Kind() string {
	if c.IsText {
		return "prompt"
	}
	hasResult, hasText := false, false
	for _, bl := range c.Blocks {
		switch bl.Type {
		case "tool_result":
			hasResult = true
		case "text":
			hasText = true
		}
	}
	switch {
	case hasResult && !hasText:
		return "tool_result"
	case hasResult && hasText:
		return "mixed"
	default:
		return "prompt"
	}
}

// ToolUses returns the tool_use blocks in the content.
func (c Content) ToolUses() []Block {
	var out []Block
	for _, bl := range c.Blocks {
		if bl.Type == "tool_use" {
			out = append(out, bl)
		}
	}
	return out
}

// Result of a Scan call.
type Result struct {
	BytesConsumed int64 // offset just past the last complete line processed
	Lines         int   // complete lines seen (including malformed)
	Malformed     int   // lines that failed to decode
}

// ErrStop can be returned by the callback to end scanning early without error.
var ErrStop = errors.New("jsonl: stop")

// Scan reads complete lines from r, decodes each and calls fn. A trailing
// partial line (file still being appended) is NOT consumed, so the returned
// BytesConsumed can be used as the resume offset next time. Malformed lines
// are counted and skipped; they never abort the scan.
func Scan(r io.Reader, fn func(Line) error) (Result, error) {
	var res Result
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		raw, err := br.ReadBytes('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				// raw holds a partial line without newline — leave it for next time.
				return res, nil
			}
			return res, err
		}
		res.Lines++
		res.BytesConsumed += int64(len(raw))
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			continue
		}
		var ln Line
		if jerr := json.Unmarshal(trimmed, &ln); jerr != nil || ln.Type == "" {
			res.Malformed++
			continue
		}
		if cerr := fn(ln); cerr != nil {
			if errors.Is(cerr, ErrStop) {
				return res, nil
			}
			return res, cerr
		}
	}
}
