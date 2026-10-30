package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// Discord poll limits.
const (
	maxPollQuestionRunes = 300
	maxPollAnswerRunes   = 55
	minPollAnswers       = 2
	maxPollAnswers       = 10
	defaultPollHours     = 24
	maxPollHours         = 768
)

// RegisterPoll adds create_poll, which posts a native Discord poll in channelID.
func RegisterPoll(reg *Registry, session *discordgo.Session, channelID string) {
	reg.Register(&createPollTool{
		send: func(msg *discordgo.MessageSend) error {
			_, err := session.ChannelMessageSendComplex(channelID, msg)
			return err
		},
		polled: &reg.Polled,
	})
}

type createPollTool struct {
	send   func(msg *discordgo.MessageSend) error
	polled *bool
}

func (t *createPollTool) Name() string { return ToolNameCreatePoll }
func (t *createPollTool) Description() string {
	return "Post a native Discord poll in this channel, for example when people disagree about where to go or what to pick. One poll per turn."
}
func (t *createPollTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"question":{"type":"string","description":"The poll question, at most 300 characters."},` +
		`"answers":{"type":"array","items":{"type":"string"},"description":"2 to 10 answers, each at most 55 characters."},` +
		`"duration_hours":{"type":"integer","description":"How long the poll stays open, 1 to 768 hours. Default 24."},` +
		`"multiselect":{"type":"boolean","description":"Allow voting for more than one answer."}},` +
		`"required":["question","answers"]}`)
}

func (t *createPollTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if *t.polled {
		return "A poll was already posted in this turn. Do not post another.", nil
	}
	poll, problem, err := buildPoll(args)
	if err != nil {
		return "", err
	}
	if problem != "" {
		return problem, nil
	}
	err = t.send(&discordgo.MessageSend{
		Poll:            poll,
		AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeUsers}},
	})
	if err != nil {
		return "", err
	}
	*t.polled = true
	return fmt.Sprintf("Poll posted with %d answers, open for %d hours.", len(poll.Answers), poll.Duration), nil
}

// buildPoll validates create_poll arguments and clamps them to Discord's limits.
// problem is a message for the model when the arguments cannot make a poll.
func buildPoll(args json.RawMessage) (poll *discordgo.Poll, problem string, err error) {
	var p struct {
		Question      string   `json:"question"`
		Answers       []string `json:"answers"`
		DurationHours *int     `json:"duration_hours"`
		Multiselect   bool     `json:"multiselect"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, "", err
	}
	question := truncateRunes(strings.TrimSpace(p.Question), maxPollQuestionRunes)
	if question == "" {
		return nil, "Error: question is required.", nil
	}
	var answers []discordgo.PollAnswer
	for _, a := range p.Answers {
		if a = truncateRunes(strings.TrimSpace(a), maxPollAnswerRunes); a != "" {
			answers = append(answers, discordgo.PollAnswer{Media: &discordgo.PollMedia{Text: a}})
		}
	}
	if len(answers) < minPollAnswers || len(answers) > maxPollAnswers {
		return nil, fmt.Sprintf("Error: a poll needs %d to %d non-empty answers, got %d.", minPollAnswers, maxPollAnswers, len(answers)), nil
	}
	hours := defaultPollHours
	if p.DurationHours != nil {
		hours = min(max(*p.DurationHours, 1), maxPollHours)
	}
	return &discordgo.Poll{
		Question:         discordgo.PollMedia{Text: question},
		Answers:          answers,
		AllowMultiselect: p.Multiselect,
		LayoutType:       discordgo.PollLayoutTypeDefault,
		Duration:         hours,
	}, "", nil
}

func truncateRunes(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit])
}
