package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/tomasmach/vespra/tools"
)

func TestReplyWithTableReadsWellInDiscord(t *testing.T) {
	content, err := os.ReadFile("testdata/leaderboard_reply.md")
	if err != nil {
		t.Fatal(err)
	}
	var sent []string
	send := func(s string) error {
		sent = append(sent, s)
		return nil
	}
	r := tools.NewDefaultRegistry(nil, "", 0, 0, send, func(string) error { return nil }, nil, nil, 2)
	args, _ := json.Marshal(map[string]string{"content": string(content)})
	if _, err := r.Dispatch(context.Background(), "reply", args); err != nil {
		t.Fatal(err)
	}

	if len(sent) != 2 {
		t.Fatalf("sent %d messages, want 2", len(sent))
	}
	for i, part := range sent {
		if utf16Units(part) > 2000 {
			t.Errorf("part %d has %d UTF-16 units, over Discord's 2000 limit", i, utf16Units(part))
		}
		for _, line := range strings.Split(part, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "|") {
				t.Errorf("part %d still has a raw table line: %q", i, line)
			}
		}
	}
	if !strings.HasSuffix(sent[0], "\n") {
		t.Errorf("first part ends mid-line: %q", sent[0][len(sent[0])-40:])
	}
	joined := strings.Join(sent, "")
	for _, want := range []string{
		"**Claude Fable 5.2**\n- Výrobce: Anthropic (9. 11.)\n- Intelligence Index (Artificial Analysis): **65** (max effort) / 62 (high)\n- BenchLeader index: 70,5 – **#1**\n",
		"(zlevnění z 10/50)\n\n**GPT-6.2 Astra**\n- Výrobce: OpenAI\n- Intelligence Index (Artificial Analysis): 62\n- Poznámka: OpenAI",
		"Opus 5.5 nebo Fable 5.2 budou dál.",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("reply is missing %q", want)
		}
	}
}

func TestConvertTables(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "two columns become name and value",
			in:   "Ceny:\n\n| Model | Cena |\n|:--|--:|\n| Sol | $2/$10 |\n| Fable | $8/$40 |\n\nTo je vše.",
			want: "Ceny:\n\n- **Sol**: $2/$10\n- **Fable**: $8/$40\n\nTo je vše.",
		},
		{
			name: "table without outer pipes",
			in:   "Model | Cena\n--- | ---\nSol | $2",
			want: "- **Sol**: $2",
		},
		{
			name: "empty cells are dropped",
			in:   "| Model | Skóre | Pozn. |\n|---|---|---|\n| Astra | — | levný |\n| | 61 | |",
			want: "**Astra**\n- Pozn.: levný\n\n- Skóre: 61",
		},
		{
			name: "table in code block stays",
			in:   "```\n| a | b |\n|---|---|\n| 1 | 2 |\n```",
			want: "```\n| a | b |\n|---|---|\n| 1 | 2 |\n```",
		},
		{
			name: "pipe in text without delimiter row stays",
			in:   "ano | ne\n---\ndalší",
			want: "ano | ne\n---\ndalší",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tools.ConvertTables(tt.in); got != tt.want {
				t.Errorf("ConvertTables() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestSplitMessageBreaksAtLineThenSpace(t *testing.T) {
	para := strings.Repeat("slovo ", 200)
	msg := para + "\n" + para
	parts := tools.SplitMessage(msg, 2000)
	if len(parts) != 2 || parts[0] != para+"\n" {
		t.Errorf("want split after the first paragraph, got parts of %d and %d chars", len(parts[0]), len(parts[len(parts)-1]))
	}

	words := strings.Repeat("abcdef ", 400)
	parts = tools.SplitMessage(words, 2000)
	if strings.Join(parts, "") != words {
		t.Fatal("SplitMessage() lost content")
	}
	for i, p := range parts[:len(parts)-1] {
		if !strings.HasSuffix(p, " ") {
			t.Errorf("part %d ends mid-word: %q", i, p[len(p)-10:])
		}
	}
}

func TestFormatForDiscordKeepsTextThatFitsTheCap(t *testing.T) {
	tests := []struct {
		name     string
		msg      string
		maxParts int
		atSpace  bool
	}{
		{"early line break would overflow", strings.Repeat("x", 1001) + "\n" + strings.Repeat("abcdef ", 380), 2, true},
		{"space right at the last fitting spot", strings.Repeat("abc ", 497) + "x " + strings.Repeat("b", 20) + strings.Repeat(" xyz", 495), 2, true},
		{"three full parts", strings.Repeat("abcd ", 399) + "abc " + strings.Repeat("abcd ", 400) + strings.Repeat("abcd ", 399) + "abcde", 3, true},
		{"emoji at part edges", strings.Repeat("a", 1499) + "\n" + strings.Repeat("b", 1999) + "😀" + strings.Repeat("c", 1997) + "😀" + strings.Repeat("d", 1999), 4, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts := tools.FormatForDiscord(tt.msg, 2000, tt.maxParts)
			if len(parts) > tt.maxParts || strings.Join(parts, "") != tt.msg {
				t.Fatalf("want the whole %d-unit message in %d parts, got %d parts", utf16Units(tt.msg), tt.maxParts, len(parts))
			}
			for i, p := range parts {
				if utf16Units(p) > 2000 {
					t.Errorf("part %d has %d UTF-16 units", i, utf16Units(p))
				}
			}
			for i, p := range parts[:len(parts)-1] {
				if tt.atSpace && !strings.HasSuffix(p, " ") {
					t.Errorf("part %d ends mid-word: %q", i, p[len(p)-10:])
				}
			}
		})
	}
}

func TestFormatForDiscordTruncatedPartFitsLimit(t *testing.T) {
	parts := tools.FormatForDiscord(strings.Repeat("abcdef ", 1000), 2000, 2)
	if len(parts) != 2 || !strings.HasSuffix(parts[1], "…") {
		t.Fatalf("want 2 parts ending with …, got %d", len(parts))
	}
	if !strings.HasSuffix(parts[0], " ") {
		t.Errorf("first part ends mid-word: %q", parts[0][len(parts[0])-10:])
	}
	for i, p := range parts {
		if utf16Units(p) > 2000 {
			t.Errorf("part %d has %d UTF-16 units, over Discord's 2000 limit", i, utf16Units(p))
		}
	}
}
