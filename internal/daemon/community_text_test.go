package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/catgirl-systems/oto/internal/soulseek"
	"github.com/catgirl-systems/oto/internal/storage/db"
)

func TestCommunityTextRules(t *testing.T) {
	p, err := compileCommunityTextTools(CommunityTextTools{Keywords: []string{"世界"}, Censorship: []string{"alice*", "b?d", "[literal]"}, Substitutions: []CommunitySubstitution{{From: "foo", To: "bar"}, {From: "bar", To: "世界"}}})
	must(t, err)
	if out, err := p.outgoing("foo\nfoo"); err != nil || out != "世界 世界" {
		t.Fatal(out, err)
	}
	if out, mention := p.incoming("ALICE! bad\t[literal]\n世界", "alice"); out != "*** ***\t***\n世界" || !mention {
		t.Fatal(out, mention)
	}
	for _, text := range []string{"malice", "alice2", "alice_", "alice\u0301", "xalice alicey"} {
		failIf(t, containsCommunityMention(text, "alice"), "false mention", text)
	}
	for _, text := range []string{"@Alice!", "ALICE", "xalice, alice.", "(alice)"} {
		failIf(t, !containsCommunityMention(text, "alice"), "missing mention", text)
	}
	failIf(t, !containsCommunityMention("İ!", "i"), "Unicode mention")
	p, err = compileCommunityTextTools(CommunityTextTools{Censorship: []string{"*"}, Substitutions: []CommunitySubstitution{{From: "a", To: strings.Repeat("b", 1024)}}})
	must(t, err)
	if out, _ := p.incoming("a  b\n", ""); out != "***  ***\n" {
		t.Fatal("whitespace changed", out)
	}
	if _, err := p.outgoing(strings.Repeat("a", soulseek.MaxChatBytes)); err == nil {
		t.Fatal("oversize expansion accepted")
	}
	for _, settings := range []CommunityTextTools{{Keywords: []string{""}}, {Substitutions: []CommunitySubstitution{{From: "", To: "x"}}}, {Censorship: []string{"two words"}}, {Keywords: make([]string, 33)}} {
		if _, err := compileCommunityTextTools(settings); err == nil {
			t.Fatal("invalid settings accepted")
		}
	}
}
func TestCommunityTextPersistenceAndSubmissionIdentity(t *testing.T) {
	s := downloadService(t)
	ctx := context.Background()
	_, _, id := communityTestConnection(t, s)
	p, err := compileCommunityTextTools(CommunityTextTools{Keywords: []string{"secret"}, Censorship: []string{"secret"}, Substitutions: []CommunitySubstitution{{From: "hello", To: "goodbye"}}})
	must(t, err)
	s.mu.Lock()
	s.community.text = p
	s.mu.Unlock()
	must(t, s.receiveCommunityPrivate(ctx, id, soulseek.PrivateMessage{Username: "Alice", ID: 7, Text: "secret"}))
	conv, err := s.stateDB.Queries().FindCommunityConversation(ctx, db.FindCommunityConversationParams{Account: id.Account, Kind: "private", Target: "Alice"})
	must(t, err)
	page, err := s.CommunityMessages(ctx, CommunityMessagesRequest{CommunityIdentity: id, ConversationID: conv.ID})
	must(t, err)
	failIf(t, len(page.Messages) != 1 || page.Messages[0].Text != "***" || !page.Messages[0].Mention, page)
	req := CommunitySendRequest{CommunityIdentity: id, Username: "Alice", Text: "hello", RequestID: "transform"}
	sent, err := s.SendCommunityPrivate(ctx, req)
	must(t, err)
	row, err := s.stateDB.Queries().GetCommunityMessage(ctx, db.GetCommunityMessageParams{Account: id.Account, ID: sent.MessageID})
	failIf(t, err != nil || row.Body != "goodbye", row, err)
	s.mu.Lock()
	s.community.text = nil
	s.mu.Unlock()
	repeat, err := s.SendCommunityPrivate(ctx, req)
	failIf(t, err != nil || !repeat.Duplicate || repeat.MessageID != sent.MessageID, repeat, err)
}
