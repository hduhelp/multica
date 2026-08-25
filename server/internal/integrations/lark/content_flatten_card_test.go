package lark

import (
	"strings"
	"testing"
)

// A card is how an agent's own previous answer comes back in the next turn's
// context, so collapsing it to "[interactive card]" made every turn lose the
// last one's conclusions. These cover the two schemas we send and receive.
func TestFlattenInteractiveCardRecoversText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "schema 2.0 markdown body",
			content: `{"schema":"2.0","header":{"title":{"tag":"plain_text","content":"MR 已提"}},"body":{"elements":[{"tag":"markdown","content":"改动内容：移除硬编码的 X-Use-Ppe"}]}}`,
			want:    "[interactive card]\nMR 已提\n改动内容：移除硬编码的 X-Use-Ppe",
		},
		{
			name:    "schema 1.0 elements",
			content: `{"elements":[{"tag":"div","text":{"tag":"lark_md","content":"根因定位了"}}]}`,
			want:    "[interactive card]\n根因定位了",
		},
		{
			name:    "no readable text",
			content: `{"elements":[{"tag":"hr"}]}`,
			want:    "[interactive card]",
		},
		{
			name:    "malformed json degrades",
			content: `not json`,
			want:    "[interactive card]",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := flattenContent("interactive", tc.content, "om_1"); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// An agent's analysis card can be thousands of characters and the context
// window carries several messages, so an uncapped card would crowd out the
// rest of the prompt.
func TestFlattenInteractiveCardCaps(t *testing.T) {
	t.Parallel()
	long := make([]rune, maxCardTextRunes+200)
	for i := range long {
		long[i] = '字'
	}
	got := flattenContent("interactive", `{"elements":[{"text":{"content":"`+string(long)+`"}}]}`, "om_1")
	if len([]rune(got)) > maxCardTextRunes+64 {
		t.Errorf("card not capped: %d runes", len([]rune(got)))
	}
	if !strings.Contains(got, "[card truncated]") {
		t.Error("truncation must be visible so the agent knows text was dropped")
	}
}
