package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	// Discord returns this many search results per page.
	searchPageSize  = 25
	maxSearchOffset = 9975
	// searchAttempts and maxIndexWait bound waiting while Discord is still indexing the server.
	searchAttempts     = 3
	maxIndexWait       = 5 * time.Second
	discordEpochMillis = 1420070400000
	minSnowflakeDigits = 17
)

// RegisterMessageSearch adds search_messages, which searches the whole history
// of channelID through Discord's guild message search. mentions are the users
// mentioned in the messages being answered.
func RegisterMessageSearch(reg *Registry, session *discordgo.Session, guildID, channelID string, mentions []*discordgo.User) {
	reg.Register(&searchMessagesTool{guildID: guildID, channelID: channelID, session: session, mentions: mentions})
}

type searchMessagesTool struct {
	guildID   string
	channelID string
	session   *discordgo.Session
	mentions  []*discordgo.User
}

func (t *searchMessagesTool) Name() string { return ToolNameSearchMessages }
func (t *searchMessagesTool) Description() string {
	return "Search the whole history of this channel, including messages far older than this conversation. Use it when someone asks what was said, who said something, or when. Filters combine. Times are local; for a single day set after to that day and before to the next day. Returns 25 matches per page, newest first."
}
func (t *searchMessagesTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` +
		`"content":{"type":"string","description":"Words the messages must contain."},` +
		`"author":{"type":"string","description":"Only messages written by this person: username as shown in the chat, or user ID."},` +
		`"mentions":{"type":"string","description":"Only messages that mention this person: username as shown in the chat, or user ID."},` +
		`"after":{"type":"string","description":"Only messages sent at or after this local time, YYYY-MM-DD or YYYY-MM-DD HH:MM."},` +
		`"before":{"type":"string","description":"Only messages sent before this local time, YYYY-MM-DD or YYYY-MM-DD HH:MM."},` +
		`"has":{"type":"string","enum":["image","video","sound","file","link","embed","sticker","poll"],"description":"Only messages with this kind of content."},` +
		`"oldest_first":{"type":"boolean","description":"Return the oldest matches first."},` +
		`"offset":{"type":"integer","description":"Number of matches to skip, for the next page."}}}`)
}

func (t *searchMessagesTool) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Content     string `json:"content"`
		Author      string `json:"author"`
		Mentions    string `json:"mentions"`
		After       string `json:"after"`
		Before      string `json:"before"`
		Has         string `json:"has"`
		OldestFirst bool   `json:"oldest_first"`
		Offset      int    `json:"offset"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	// Only this channel is searched, and everyone here can read it, so age-restricted channels are fine.
	query := url.Values{"channel_id": {t.channelID}, "include_nsfw": {"true"}}
	if p.Content != "" {
		query.Set("content", p.Content)
	}
	for param, name := range map[string]string{"author_id": p.Author, "mentions": p.Mentions} {
		if name == "" {
			continue
		}
		userID, err := t.memberID(ctx, name)
		if err != nil {
			return "", err
		}
		query.Set(param, userID)
	}
	if p.After != "" {
		id, err := snowflakeAt(p.After)
		if err != nil {
			return "", err
		}
		// min_id is exclusive, so start one below the first ID of that time.
		query.Set("min_id", strconv.FormatInt(id-1, 10))
	}
	if p.Before != "" {
		id, err := snowflakeAt(p.Before)
		if err != nil {
			return "", err
		}
		query.Set("max_id", strconv.FormatInt(id, 10))
	}
	if p.Has != "" {
		query.Set("has", p.Has)
	}
	if p.OldestFirst {
		query.Set("sort_order", "asc")
	}
	if p.Offset > 0 {
		query.Set("offset", strconv.Itoa(p.Offset))
	}

	body, err := t.search(ctx, query)
	if err != nil {
		return "", err
	}
	var resp struct {
		TotalResults int                    `json:"total_results"`
		Messages     [][]*discordgo.Message `json:"messages"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("decode search results: %w", err)
	}
	msgs := slices.Concat(resp.Messages...)
	if len(msgs) == 0 {
		return "No messages match.", nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d messages match. Showing %d-%d", resp.TotalResults, p.Offset+1, p.Offset+len(msgs))
	if next := p.Offset + searchPageSize; next < resp.TotalResults {
		if next <= maxSearchOffset {
			fmt.Fprintf(&sb, "; call again with offset %d for more", next)
		} else {
			sb.WriteString("; Discord pages no further, so narrow the time range for more")
		}
	}
	sb.WriteString(".\n")
	for _, m := range msgs {
		fmt.Fprintf(&sb, "[%s] %s: %s", m.Timestamp.In(time.Local).Format("2006-01-02 15:04"), m.Author.Username, m.ContentWithMentionsReplaced())
		for _, a := range m.Attachments {
			fmt.Fprintf(&sb, " [attachment %s]", a.Filename)
		}
		fmt.Fprintf(&sb, " https://discord.com/channels/%s/%s/%s\n", t.guildID, m.ChannelID, m.ID)
	}
	return sb.String(), nil
}

// search calls Discord's message search, waiting and retrying while Discord
// answers 202 because it has not indexed the server yet.
func (t *searchMessagesTool) search(ctx context.Context, query url.Values) ([]byte, error) {
	uri := discordgo.EndpointGuild(t.guildID) + "/messages/search"
	for attempt := 1; ; attempt++ {
		body, err := t.session.RequestWithBucketID(http.MethodGet, uri+"?"+query.Encode(), nil, uri, discordgo.WithContext(ctx))
		var restErr *discordgo.RESTError
		if attempt == searchAttempts || !errors.As(err, &restErr) || restErr.Response.StatusCode != http.StatusAccepted {
			return body, err
		}
		var notReady struct {
			RetryAfter float64 `json:"retry_after"`
		}
		json.Unmarshal(restErr.ResponseBody, &notReady) //nolint:errcheck // a missing retry_after means the default wait
		wait := time.Duration(notReady.RetryAfter * float64(time.Second))
		if wait > maxIndexWait {
			return body, err
		}
		if wait <= 0 {
			wait = time.Second
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// memberID returns the user ID of the server member called name, which may be
// a user ID, username, nickname or display name.
func (t *searchMessagesTool) memberID(ctx context.Context, name string) (string, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "@")
	if _, err := strconv.ParseUint(name, 10, 64); err == nil && len(name) >= minSnowflakeDigits {
		return name, nil
	}
	// The model sees mentions as display names, which member search does not match.
	var mentioned []string
	for _, u := range t.mentions {
		if strings.EqualFold(u.Username, name) {
			return u.ID, nil
		}
		if strings.EqualFold(u.GlobalName, name) && !slices.Contains(mentioned, u.ID) {
			mentioned = append(mentioned, u.ID)
		}
	}
	switch len(mentioned) {
	case 0:
	case 1:
		return mentioned[0], nil
	default:
		return "", fmt.Errorf("%q is the display name of several mentioned people; call again with one exact username", name)
	}
	members, err := t.session.GuildMembersSearch(t.guildID, name, 10, discordgo.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("search members: %w", err)
	}
	var named []*discordgo.Member
	usernames := make([]string, 0, len(members))
	for _, m := range members {
		if strings.EqualFold(m.User.Username, name) {
			return m.User.ID, nil
		}
		if strings.EqualFold(m.Nick, name) || strings.EqualFold(m.User.GlobalName, name) {
			named = append(named, m)
		}
		usernames = append(usernames, m.User.Username)
	}
	switch {
	case len(named) == 1:
		return named[0].User.ID, nil
	case len(named) == 0 && len(members) == 1:
		return members[0].User.ID, nil
	case len(members) == 0:
		return "", fmt.Errorf("no member of this server is called %q; use their username as shown in the chat", name)
	}
	return "", fmt.Errorf("%q matches several members (%s); call again with one exact username", name, strings.Join(usernames, ", "))
}

// snowflakeAt returns the Discord ID of the first possible message at the local time value.
func snowflakeAt(value string) (int64, error) {
	t, err := time.ParseInLocation("2006-01-02 15:04", value, time.Local)
	if err != nil {
		t, err = time.ParseInLocation("2006-01-02", value, time.Local)
	}
	if err != nil {
		return 0, fmt.Errorf("%q is not YYYY-MM-DD or YYYY-MM-DD HH:MM", value)
	}
	return (t.UnixMilli() - discordEpochMillis) << 22, nil
}
