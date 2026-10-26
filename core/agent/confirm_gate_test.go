package agent

import (
	"slices"
	"testing"
)

// #152: only a message that asks the session to push, merge, publish or deploy is gated. One
// that forbids the step, or asks whether it happened, is an ordinary message.
func TestGatedActions(t *testing.T) {
	for _, tc := range []struct {
		text string
		want []string
	}{
		// Asks for the step.
		{"Commit, push, open one PR covering #137 and #138, and merge it.", []string{"push", "merge"}},
		{"Go ahead and git push.", []string{"push"}},
		{"Please push your branch.", []string{"push"}},
		{"Run the tests, then push.", []string{"push"}},
		{"Don't wait for CI, just push it.", []string{"push"}},
		{"Don't forget to push.", []string{"push"}},
		{"Don't change anything else, but deploy the site.", []string{"deploy"}},
		{"Can you publish the package?", []string{"publish"}},
		{"Make sure everything is pushed.", []string{"push"}},
		{"Have you pushed? If not, push it now.", []string{"push"}},
		// The coordinator's messages from #152, verbatim: none asks for a push or a merge.
		{"Please bring this folder up to date with origin/main using a fast-forward only: git pull --ff-only. First check git status; if there are any uncommitted changes or the fast-forward is not possible, stop and tell me instead of merging, rebasing or resetting. Do not push anything. Tell me the final status when done.", nil},
		{"Question only, please don't change anything: have you built a build of current main that includes the merged onboarding window (PR 150)? The latest release, v0.8.0, was made before it. If there is no build of current main, can you tell me why? Just answer here in your terminal; don't update the folder, build, or push anything.", nil},
		{"Question only, please don't change anything: has anything from your work been pushed to GitHub, and is there anything of yours that is still not pushed? Just answer here in your terminal; don't push, commit, or change anything.", nil},
		{"Is there anything committed on this branch? Please don't push anything or change anything, just tell me what's committed.", nil},
		// Other ways of saying no, or of asking what happened.
		{"Never push to main.", nil},
		{"Fix the test without pushing.", nil},
		{"You can’t merge it yet.", nil},
		{"Did you deploy it?", nil},
		{"Run the tests and tell me what fails.", nil},
		// #165: a question about where things stand, and the step named as a thing, ask for nothing.
		{"Where are we on the commit, the PR and the merge?", nil},
		{"What's the status of the push?", nil},
		{"How did the deploy go?", nil},
		{"Where's the push at?", nil},
		{"Merge the PR.", []string{"merge"}},
		{"Do the merge.", []string{"merge"}},
		{"How about you merge it?", []string{"merge"}},
		{"When can you push?", []string{"push"}},
		// #246: the sender reporting their own step asks the session for nothing.
		{"Heads-up: PR #243 is now merged into main. Before merging I merged main into your branch. Pull main before you change anything else.", nil},
		{"I pushed the fix to origin, so pull before you start.", nil},
		{"We're merging it here now; don't push to that branch meanwhile.", nil},
		{"I'll deploy it myself tonight.", []string{"deploy"}}, // a plan asks all the same
		{"I want you to merge it.", []string{"merge"}},
		{"I merged main; now push your branch.", []string{"push"}},
		{"We just pushed the fix; pull it.", nil},
		{"I'm merging it here.", nil},
		// Asks all the same: a plan, an opinion or an approval from the sender is a request.
		{"We should merge it now.", []string{"merge"}},
		{"I think we should push.", []string{"push"}},
		{"We can merge now.", []string{"merge"}},
		{"We push now.", []string{"push"}},
		{"I'd merge it.", []string{"merge"}},
		{"I say push it.", []string{"push"}},
		{"I approve, merge it.", []string{"merge"}},
		{"I've reviewed, merge and push.", []string{"merge", "push"}},
		{"We merge and push.", []string{"merge", "push"}},
		{"I need you to push.", []string{"push"}},
		{"Let's push.", []string{"push"}},
		{"After merging, push.", []string{"merge", "push"}},
		// #255: "push" describing something that moves on screen, or naming a kind of thing, asks
		// for nothing.
		{"Can we have two separate bubbles, the one that's from me and the one that's from Pixie, and the latest one shows at the bottom and pushes the older one up, and the one on the top fades after four or five seconds?", nil},
		{"on the Pixie one the one that's working on the caption can we maybe have two separate bubble the one that's from me and the one that's from Pixie and you know the latest one show on the bottom and it pushes up and the one that's on the top fades", nil},
		{"A new row comes in at the top, pushing the others down.", nil},
		{"Turn on push notifications for finished jobs.", nil},
		{"Add push to talk.", nil},
		// … while an instruction to push still asks, whatever follows it: asking once too often is
		// the safe side.
		{"Push it up.", []string{"push"}},
		{"Push up the changes.", []string{"push"}},
		{"Push the branch up to origin.", []string{"push"}},
		{"Force push the branch.", []string{"push"}},
		{"Let's get to your landing push. It looks good.", []string{"push"}},
		{"Push the fix and then push the notification change too.", []string{"push"}},
		{"Push your work up.", []string{"push"}},
		{"Push the release up.", []string{"push"}},
		{"Push it over to staging.", []string{"push"}},
		{"Push it back.", []string{"push"}},
		{"Push the update along.", []string{"push"}},
		{"Push these up.", []string{"push"}},
		{"Push the latest up and merge.", []string{"push", "merge"}},
		{"Pushing it up now please.", []string{"push"}},
		{"It pushes it up when done.", []string{"push"}},
		{"Make sure it's pushed up.", []string{"push"}},
		{"Keep pushing up the changes.", []string{"push"}},
		{"Push back if you think the design is wrong.", []string{"push"}},
		{"Push the older bubble down when a new one comes in.", []string{"push"}},
		// #329: a step that waits for the sender's own review asks for nothing yet: they're
		// asked again when they give the go-ahead.
		{"Based on the analysis we have, update the landing page to really show our strength against other players. Have me review it before you push that.", nil},
		{"Let me see it before you merge.", nil},
		{"Wait for my OK before pushing.", nil},
		{"Check with me before you deploy.", nil},
		{"I'll review it before we publish.", nil},
		{"Push it once I've reviewed it.", nil},
		{"Merge after I approve.", nil},
		// … while one that waits for something else, or comes before the review, still asks.
		{"Run the tests before you push.", []string{"push"}},
		{"Send me a summary before you push.", []string{"push"}},
		{"I want you to fix it before you push.", []string{"push"}},
		{"Push it, then let me review.", []string{"push"}},
		{"Push after the tests pass.", []string{"push"}},
		{"Have me review it before you push, then merge.", []string{"merge"}},
		{"Merge, then push once I've reviewed it.", []string{"merge"}},
		{"Let me look at it first before you push.", nil},
		{"Merge after my approval.", nil},
		// … and the sender or a review named elsewhere in the sentence isn't the wait.
		{"I've reviewed it, rebase before you push.", []string{"push"}},
		{"I've checked it, squash the commits before you push.", []string{"push"}},
		{"We've seen the diff, squash it before you merge.", []string{"merge"}},
		{"OK I think it's good, run lint before you push.", []string{"push"}},
		{"We need to see green CI before you deploy.", []string{"deploy"}},
		{"Let our CI check it before you push.", []string{"push"}},
		{"Skip my review before you push.", []string{"push"}},
		{"Push when you're done, I'll look at it tomorrow.", []string{"push"}},
		{"Push it once the tests pass and I'll check it later.", []string{"push"}},
		{"Deploy when ready and send me the URL so I can check it.", []string{"deploy"}},
		{"Push it when we see green CI.", []string{"push"}},
		{"Keep deploying every hour until I say stop.", []string{"deploy"}},
		{"Publish the release when you see my comment is resolved.", []string{"publish"}},
		{"Push to the branch and merge once I've approved it.", []string{"push"}},
		{"Push it and open a PR, merge when I approve.", []string{"push"}},
		{"Hold the push until I say so.", []string{"push"}},
		{"Keep deploying every hour until I say so.", []string{"deploy"}},
		{"Keep pushing your fixes until I approve the PR.", []string{"push"}},
		{"Keep pushing after I approve each one.", []string{"push"}},
		{"Go ahead and merge it, nothing changed after I approved it.", []string{"merge"}},
		{"Push it, the tests passed when I checked.", []string{"push"}},
		{"Deploy it, it looked fine when I looked at staging.", []string{"deploy"}},
		{"Merge once my review comments are addressed.", []string{"merge"}},
		{"Push after my review bot passes.", []string{"push"}},
		{"If you're unsure, ask me before you push.", []string{"push"}},
		{"If CI fails, check with me before you deploy.", []string{"deploy"}},
		{"Push after I check in my changes.", []string{"push"}},
		{"Merge after I have reviewed it.", nil},
		{"Push after my OK, then merge.", []string{"merge"}},
		{"Ask me before you push unless it's a typo fix.", []string{"push"}},
		{"Check with me before you deploy if anything looks off.", []string{"deploy"}},
		{"Also, if you're unsure, ask me before you push.", []string{"push"}},
		{"Fix the bug, and if you're unsure, ask me before you push.", []string{"push"}},
		{"When in doubt, ask me before you push.", []string{"push"}},
		{"Push it, it works when I check.", []string{"push"}},
		{"Deploy it, it looks fine when I look at it locally.", []string{"deploy"}},
		{"Push the changes once I've reviewed them.", nil},
		{"Ask me before you push when you're unsure.", []string{"push"}},
		{"Check with me before you deploy when it touches billing.", []string{"deploy"}},
		{"Check with me before you merge anything big.", []string{"merge"}},
		{"Ask me before you deploy to prod, staging is fine.", []string{"deploy"}},
		{"Ask me before you push to main, other branches are fine.", []string{"push"}},
		{"Have me review the changes before pushing them.", nil},
	} {
		if got := GatedActions(tc.text); !slices.Equal(got, tc.want) {
			t.Errorf("GatedActions(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

// #165: a pix description ("deskpixie: fix the gate") is said, not the session's name.
func TestTheMessageQuestionSaysTheSessionsDescription(t *testing.T) {
	in := map[string]any{"name": "deskpixie-claude", "description": "deskpixie: fix the gate", "actions": []string{"merge"}}
	if got, want := Describe(MessageSessionTool, in, Coordinator), "Message your session deskpixie, fix the gate, asking it to merge?"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := Describe(CloseSessionTool, in, Coordinator), "Close your session deskpixie, fix the gate?"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
