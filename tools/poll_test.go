package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

func TestBuildPoll(t *testing.T) {
	tests := []struct {
		name        string
		args        string
		wantProblem bool
		wantAnswers int
		wantHours   int
		wantMulti   bool
	}{
		{name: "defaults", args: `{"question":"Lokál or Vinohradský?","answers":["Lokál","Vinohradský"]}`, wantAnswers: 2, wantHours: 24},
		{name: "multiselect and duration", args: `{"question":"Q","answers":["a","b","c"],"duration_hours":48,"multiselect":true}`, wantAnswers: 3, wantHours: 48, wantMulti: true},
		{name: "duration clamped up", args: `{"question":"Q","answers":["a","b"],"duration_hours":0}`, wantAnswers: 2, wantHours: 1},
		{name: "duration clamped down", args: `{"question":"Q","answers":["a","b"],"duration_hours":5000}`, wantAnswers: 2, wantHours: 768},
		{name: "blank answers dropped", args: `{"question":"Q","answers":["a"," ","b"]}`, wantAnswers: 2, wantHours: 24},
		{name: "missing question", args: `{"question":"  ","answers":["a","b"]}`, wantProblem: true},
		{name: "one answer", args: `{"question":"Q","answers":["a"]}`, wantProblem: true},
		{name: "eleven answers", args: `{"question":"Q","answers":["1","2","3","4","5","6","7","8","9","10","11"]}`, wantProblem: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			poll, problem, err := buildPoll(json.RawMessage(tt.args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantProblem {
				if problem == "" || poll != nil {
					t.Fatalf("want problem, got poll %+v", poll)
				}
				return
			}
			if problem != "" {
				t.Fatalf("unexpected problem: %s", problem)
			}
			if len(poll.Answers) != tt.wantAnswers || poll.Duration != tt.wantHours || poll.AllowMultiselect != tt.wantMulti {
				t.Errorf("got answers=%d hours=%d multi=%v", len(poll.Answers), poll.Duration, poll.AllowMultiselect)
			}
		})
	}

	t.Run("clamps by runes", func(t *testing.T) {
		args, _ := json.Marshal(map[string]any{
			"question": strings.Repeat("ž", 400),
			"answers":  []string{strings.Repeat("🍺", 80), "b"},
		})
		poll, problem, err := buildPoll(args)
		if err != nil || problem != "" {
			t.Fatalf("err=%v problem=%q", err, problem)
		}
		if n := utf8.RuneCountInString(poll.Question.Text); n != 300 {
			t.Errorf("question runes = %d, want 300", n)
		}
		if a := poll.Answers[0].Media.Text; utf8.RuneCountInString(a) != 55 || !utf8.ValidString(a) {
			t.Errorf("answer runes = %d valid=%v, want 55 valid", utf8.RuneCountInString(a), utf8.ValidString(a))
		}
	})
}

func TestCreatePollToolSendsOnce(t *testing.T) {
	var sent []*discordgo.MessageSend
	reg := NewRegistry()
	reg.Register(&createPollTool{send: func(msg *discordgo.MessageSend) error {
		sent = append(sent, msg)
		return nil
	}, polled: &reg.Polled})

	args := json.RawMessage(`{"question":"Lokál or Vinohradský?","answers":["Lokál","Vinohradský"]}`)
	for range 2 {
		if _, err := reg.Dispatch(context.Background(), ToolNameCreatePoll, args); err != nil {
			t.Fatal(err)
		}
	}
	if len(sent) != 1 || !reg.Polled {
		t.Fatalf("sent %d polls, Polled=%v; want 1 and true", len(sent), reg.Polled)
	}
	am := sent[0].AllowedMentions
	if am == nil || len(am.Parse) != 1 || am.Parse[0] != discordgo.AllowedMentionTypeUsers {
		t.Errorf("allowed mentions = %+v, want users only", am)
	}
}
