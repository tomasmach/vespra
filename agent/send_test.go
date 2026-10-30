package agent

import (
	"encoding/json"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestReplyReference(t *testing.T) {
	guildMsg := &discordgo.Message{ID: "m1", ChannelID: "c1", GuildID: "g1"}
	dmMsg := &discordgo.Message{ID: "m2", ChannelID: "c2"}
	tests := []struct {
		name    string
		trigger *discordgo.Message
		newer   bool
		want    bool
	}{
		{"server channel with newer messages", guildMsg, true, true},
		{"server channel without newer messages", guildMsg, false, false},
		{"DM with newer messages", dmMsg, true, false},
		{"no trigger", nil, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ref := replyReference(tt.trigger, tt.newer)
			if (ref != nil) != tt.want {
				t.Fatalf("reference = %+v, want reply %v", ref, tt.want)
			}
			if ref == nil {
				return
			}
			if ref.MessageID != "m1" || ref.ChannelID != "c1" || ref.GuildID != "g1" {
				t.Errorf("reference = %+v, want message m1 in c1/g1", ref)
			}
			if ref.FailIfNotExists == nil || *ref.FailIfNotExists {
				t.Errorf("FailIfNotExists = %v, want false", ref.FailIfNotExists)
			}
		})
	}
}

func TestSafeMentionsAllowsOnlyUsers(t *testing.T) {
	data, err := json.Marshal(safeMentions())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), `{"parse":["users"],"replied_user":true}`; got != want {
		t.Errorf("allowed_mentions = %s, want %s", got, want)
	}
}
