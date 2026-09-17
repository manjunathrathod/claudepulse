package jsonl

import (
	"strings"
	"testing"
	"time"
)

func TestScanTolerance(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"mode","mode":"normal"}`,
		`{"type":"user","uuid":"u1","timestamp":"2026-09-01T10:00:00.000Z","message":{"role":"user","content":"hi"}}`,
		`{malformed`,
		``,
		`{"no_type":true}`,
		`{"type":"weird","timestamp":1788602400000}`,
		`{"type":"assistant","message":{"id":"msg_1","role":"assistant","model":"m","content":[{"type":"tool_use","id":"toolu_1","name":"Bash"}],"usage":{"input_tokens":1,"output_tokens":2,"output_tokens_details":{"thinking_tokens":1}}}}`,
	}, "\n") + "\n" + `{"type":"user","partial":` // trailing partial line, no newline

	var seen []Line
	res, err := Scan(strings.NewReader(input), func(l Line) error {
		seen = append(seen, l)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Lines != 7 {
		t.Errorf("Lines = %d, want 7 (partial line must not count)", res.Lines)
	}
	if res.Malformed != 2 {
		t.Errorf("Malformed = %d, want 2 ({malformed and no_type)", res.Malformed)
	}
	if want := int64(len(input) - len(`{"type":"user","partial":`)); res.BytesConsumed != want {
		t.Errorf("BytesConsumed = %d, want %d (stop before partial line)", res.BytesConsumed, want)
	}
	if len(seen) != 4 {
		t.Fatalf("decoded %d lines, want 4", len(seen))
	}
	if !seen[1].Timestamp.Equal(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("RFC3339 timestamp not parsed: %v", seen[1].Timestamp)
	}
	if !seen[1].Message.Content.IsText || seen[1].Message.Content.Kind() != "prompt" {
		t.Errorf("string content should be a prompt: %+v", seen[1].Message.Content)
	}
	if !seen[2].Timestamp.Equal(time.UnixMilli(1788602400000).UTC()) {
		t.Errorf("numeric timestamp not parsed: %v", seen[2].Timestamp)
	}
	a := seen[3]
	if a.Message.ID != "msg_1" || a.Message.Usage == nil || a.Message.Usage.OutputTokensDetails.ThinkingTokens != 1 {
		t.Errorf("assistant usage not decoded: %+v", a.Message)
	}
	if tu := a.Message.Content.ToolUses(); len(tu) != 1 || tu[0].Name != "Bash" || tu[0].ID != "toolu_1" {
		t.Errorf("tool_use not decoded: %+v", tu)
	}
}

func TestContentKind(t *testing.T) {
	cases := []struct {
		json, want string
	}{
		{`"typed"`, "prompt"},
		{`[{"type":"text","text":"x"}]`, "prompt"},
		{`[{"type":"tool_result","tool_use_id":"t"}]`, "tool_result"},
		{`[{"type":"tool_result","tool_use_id":"t"},{"type":"text","text":"x"}]`, "mixed"},
		{`null`, "prompt"},
		{`{"unexpected":"object"}`, "prompt"},
	}
	for _, c := range cases {
		var content Content
		if err := content.UnmarshalJSON([]byte(c.json)); err != nil {
			t.Errorf("%s: unexpected error %v", c.json, err)
			continue
		}
		if got := content.Kind(); got != c.want {
			t.Errorf("%s: Kind() = %q, want %q", c.json, got, c.want)
		}
	}
}

func TestScanStopsEarly(t *testing.T) {
	input := "{\"type\":\"a\"}\n{\"type\":\"b\"}\n{\"type\":\"c\"}\n"
	n := 0
	res, err := Scan(strings.NewReader(input), func(l Line) error {
		n++
		if l.Type == "b" {
			return ErrStop
		}
		return nil
	})
	if err != nil || n != 2 || res.Lines != 2 {
		t.Errorf("early stop: err=%v n=%d lines=%d", err, n, res.Lines)
	}
}

func TestScanHandlesHugeLine(t *testing.T) {
	big := strings.Repeat("x", 3<<20) // 3 MB single line, bigger than any scanner default
	input := `{"type":"user","uuid":"u","message":{"role":"user","content":"` + big + `"}}` + "\n"
	res, err := Scan(strings.NewReader(input), func(l Line) error { return nil })
	if err != nil || res.Lines != 1 || res.Malformed != 0 {
		t.Errorf("huge line: err=%v res=%+v", err, res)
	}
}
