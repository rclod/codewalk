package pipeline

import (
	"testing"

	"github.com/rclod/codewalk/internal/walkthrough"
)

func uncertainty(question string) walkthrough.Uncertainty {
	return walkthrough.Uncertainty{Question: question}
}

// These cases come from a real run: the author recorded an open question, and
// the grounding stage independently reported the same gap in different words.
// Listing it twice costs the reader attention and buys nothing.
func TestNearDuplicateQuestionsAreMerged(t *testing.T) {
	duplicates := [][2]string{
		{
			"How do User and Session entities written before this change behave under the new epoch check?",
			"How do `User` and `Session` entities written before this commit behave once `session_epoch` exists?",
		},
		{
			"Are new Datastore composite indexes required for the added kinds?",
			"Are new composite indexes required in Datastore for the added kinds?",
		},
	}
	for _, pair := range duplicates {
		if !similarQuestion(pair[0], pair[1]) {
			t.Errorf("expected these to be treated as the same question:\n  %s\n  %s", pair[0], pair[1])
		}
	}
}

func TestDistinctQuestionsAreKept(t *testing.T) {
	distinct := [][2]string{
		{
			"Are new Datastore composite indexes required for the added kinds?",
			"Is the OIDC audience the caller mints tokens for the same string the service requires?",
		},
		{
			"How often does the worker poll?",
			"Where is the retry interval configured?",
		},
	}
	for _, pair := range distinct {
		if similarQuestion(pair[0], pair[1]) {
			t.Errorf("these are different questions and must both survive:\n  %s\n  %s", pair[0], pair[1])
		}
	}
}

func TestAppendUncertaintyMergesAndKeeps(t *testing.T) {
	var existing []walkthrough.Uncertainty
	existing = appendUncertainty(existing, uncertainty("How do Session entities written before this change behave under the new epoch check?"))
	existing = appendUncertainty(existing, uncertainty("How do `Session` entities written before this commit behave once `session_epoch` exists?"))
	if len(existing) != 1 {
		t.Errorf("near-duplicate not merged: %d entries", len(existing))
	}
	existing = appendUncertainty(existing, uncertainty("Which service account mints the token?"))
	if len(existing) != 2 {
		t.Errorf("a genuinely new question should be kept: %d entries", len(existing))
	}
}

func TestUnsupportedClaimsBecomeQuestions(t *testing.T) {
	// A claim the repository could not support is not a question; storing it
	// verbatim in a question field reads as an assertion.
	got := asQuestion("deploy.sh now exports five HR-related environment variables")
	if got != "Could not confirm: deploy.sh now exports five HR-related environment variables" {
		t.Errorf("asQuestion = %q", got)
	}
	if asQuestion("Where is this configured?") != "Where is this configured?" {
		t.Error("an actual question should be left alone")
	}
}
