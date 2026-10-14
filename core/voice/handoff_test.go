package voice

import "testing"

func TestIsNonAnswer(t *testing.T) {
	cases := []struct {
		reply string
		want  bool
	}{
		// non-answers
		{"I don't know.", true},
		{"I don’t know.", true}, // curly apostrophe
		{"i dont know", true},
		{"Sorry, I DON'T KNOW that one.", true},
		{"Hmm, I'm not sure.", true},
		{"I’m not sure about that.", true},
		{"I am not sure what the score was.", true},
		{"I can't check that.", true},
		{"I cannot look that up right now.", true},
		{"I can't tell you that, sorry.", true},
		{"I can't access the internet.", true},
		{"I don't have access to your calendar.", true},
		{"I don't have that information.", true},
		{"I don't have real-time data, so I can't say.", true},
		{"There's no way for me to know that.", true},
		{"I'm unable to browse the web.", true},
		{"Unfortunately I'm not able to see your files.", true},
		{"I have no idea, sorry!", true},
		// real answers
		{"The capital of France is Paris.", false},
		{"It's 3 PM in Tokyo.", false},
		{"Sure, I've set a timer for ten minutes.", false},
		{"On it, I'll let you know when it's done.", false},
		{"Let me check that.", false},
		{"Paris is the capital. I'm not sure about the population.", false}, // answered first
		{"", false},
		// a guess anyway, or small talk
		{"I don't know, maybe pizza?", false},
		{"I'm not sure, probably around five.", false},
		{"I don't know, I think it's Tuesday.", false},
		{"I don't know about you, but I love rainy days.", false},
		{"I'm not sure, what do you think?", true}, // asks the user back: still a non-answer
		// asking the user to clarify
		{"I'm not sure what you mean. Could you say that again?", false},
		{"I didn't catch that, sorry. I don't know which one you mean.", false},
		// too long to be just a non-answer
		{"I'm not sure, but here is a long explanation of everything I do know about the topic, " +
			"covering its history, the main ideas, the people involved, the arguments for and " +
			"against, and some practical tips you might find useful in daily life.", false},
		// the phrase not in the first sentence
		{"Great question. I don't know.", false},
	}
	for _, c := range cases {
		if got := IsNonAnswer(c.reply); got != c.want {
			t.Errorf("IsNonAnswer(%q) = %v, want %v", c.reply, got, c.want)
		}
	}
}

func TestIsRequest(t *testing.T) {
	cases := []struct {
		said string
		want bool
	}{
		// questions and requests
		{"What's the price of bitcoin right now?", true},
		{"what is on my calendar tomorrow", true},
		{"Who won the game last night?", true},
		{"Hey Bit, can you check on the deskpixie repo?", true},
		{"Could you find me a good ramen place nearby", true},
		{"Tell me the latest news about the election.", true},
		{"Look up the weather in Lisbon next week", true},
		{"I need a summary of yesterday's meeting", true},
		{"Do you know when the store closes?", true},
		{"Okay, so how do I set up a VPN?", true},
		{"thanks, what's the score of the Giants game?", true},
		{"The new release is out today?", true},
		// small talk, acknowledgements, goodbyes
		{"Thanks!", false},
		{"Thank you so much.", false},
		{"Okay cool.", false},
		{"Got it, sounds good to me.", false},
		{"Bye!", false},
		{"Goodbye, talk to you later.", false},
		{"That's all for now, thanks.", false},
		{"No, never mind.", false},
		{"Good night Bit.", false},
		// chit-chat questions about Bit
		{"How are you doing today?", false},
		{"What's your favorite food?", false},
		{"What should we have for dinner?", false},
		{"Do you like rainy days?", false},
		{"What do you think about cats?", false},
		{"Are you a robot?", false},
		{"what's up", false},
		// too short
		{"Why?", false},
		{"Who won?", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsRequest(c.said); got != c.want {
			t.Errorf("IsRequest(%q) = %v, want %v", c.said, got, c.want)
		}
	}
}

func TestShouldHandOff(t *testing.T) {
	cases := []struct {
		user, bot string
		want      bool
	}{
		{"What's the price of bitcoin right now?", "I'm not sure, I can't check live prices.", true},
		{"What's your favorite food?", "I don't know, maybe pizza?", false},
		{"What should we have for dinner?", "I don't know.", false},
		{"Bye, thanks!", "I don't know.", false},
		{"Who won the game last night?", "The Giants won, 5 to 3.", false},
	}
	for _, c := range cases {
		if got := ShouldHandOff(c.user, c.bot); got != c.want {
			t.Errorf("ShouldHandOff(%q, %q) = %v, want %v", c.user, c.bot, got, c.want)
		}
	}
}
