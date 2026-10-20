package source_test

import (
	"encoding/json"
	"testing"

	"github.com/N0rthHXD/dmemd/internal/source"
)

func TestLocateQuoteFindsMessageAndTopLevelText(t *testing.T) {
	content := json.RawMessage(`{
		"messages": [
			{"id": "m1", "role": "user", "content": "你好世界"},
			{"id": "m2", "role": "user", "content": "second message"}
		]
	}`)

	tests := []struct {
		name      string
		messageID string
		quote     string
		field     string
		start     int
		end       int
		found     bool
	}{
		{name: "message rune offsets", messageID: "m1", quote: "世界", field: "messages.0.content", start: 2, end: 4, found: true},
		{name: "second message", messageID: "m2", quote: "second", field: "messages.1.content", start: 0, end: 6, found: true},
		{name: "global search", quote: "message", field: "messages.1.content", start: 7, end: 14, found: true},
		{name: "unknown message", messageID: "missing", quote: "世界", found: false},
		{name: "missing quote", messageID: "m1", quote: "absent", found: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			location := source.LocateQuote(content, test.messageID, test.quote)
			if !test.found {
				if location != nil {
					t.Fatalf("location = %+v, want nil", location)
				}
				return
			}
			if location == nil {
				t.Fatal("location = nil")
			}
			if location.Field != test.field || location.Start != test.start || location.End != test.end {
				t.Fatalf("location = %+v", location)
			}
		})
	}
}

func TestLocateQuoteHandlesRootStringAndInvalidJSON(t *testing.T) {
	location := source.LocateQuote(json.RawMessage(`"记忆服务"`), "", "记忆")
	if location == nil || location.Field != "" || location.Start != 0 || location.End != 2 {
		t.Fatalf("root string location = %+v", location)
	}
	if location := source.LocateQuote(json.RawMessage(`{"unterminated"`), "", "x"); location != nil {
		t.Fatalf("invalid JSON location = %+v", location)
	}
	if location := source.LocateQuote(json.RawMessage(`"text"`), "", ""); location != nil {
		t.Fatalf("empty quote location = %+v", location)
	}
}
