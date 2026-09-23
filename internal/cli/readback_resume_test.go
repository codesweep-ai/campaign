package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
)

// earlierReadback leaves a member's channels as an earlier create left them:
// dispatch d001 delivered and, when answered is set, replied to with note.
func earlierReadback(t *testing.T, state string, answered bool, note string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(state, "input", "d001.md"), []byte("the first asking"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !answered {
		return
	}
	reply, err := json.Marshal(map[string]string{"dispatch": "d001", "phase": "done", "note": note})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "replies", "d001.json"), reply, 0o600); err != nil {
		t.Fatal(err)
	}
}

// recorded is the readback the campaign record holds for the member from the
// earlier create, as runReadback saved it.
func recorded(t *testing.T, detail string) *model.Readback {
	t.Helper()
	r, err := parseReadback(goodReadback)
	if err != nil {
		t.Fatal(err)
	}
	r.At = time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC)
	r.Detail, r.Dispatch, r.Inputs = detail, "d001", map[string]string{"roles/dev.md": "brief"}
	return &r
}

// A member whose readback passed on an earlier create is not asked again. The
// deps pilot resumed create three times, and members that had passed received
// the same briefing up to four times, each headed "dispatch d001".
func TestAResumedCreateDoesNotAskAMemberThatPassed(t *testing.T) {
	a, state := readbackMemberApp(t, "unused", goodReadback)
	campaign, member := readbackCampaign()
	earlierReadback(t, state, true, goodReadback)
	member.Readback = recorded(t, "")

	var out strings.Builder
	detail, report := a.readbackOne(context.Background(), &out, campaign, member)
	if detail != "" {
		t.Fatalf("a member that passed was failed: %s\n%s", detail, out.String())
	}
	if _, err := os.Stat(filepath.Join(state, "input", "d002.md")); err == nil {
		t.Fatal("a member that had passed was asked again")
	}
	if report.Dispatch != "d001" || !report.At.Equal(member.Readback.At) {
		t.Errorf("the earlier readback must be kept as recorded; got %+v", report)
	}
	if !strings.Contains(out.String(), "confirmed its briefing in d001 on an earlier create") {
		t.Errorf("the operator must be told why the member was not asked:\n%s", out.String())
	}
}

// A member that must be asked again reads its real dispatch id and why.
func TestAResumedCreateNamesTheRealDispatchAndWhy(t *testing.T) {
	a, state := readbackMemberApp(t, "unused", goodReadback)
	campaign, member := readbackCampaign()
	earlierReadback(t, state, true, goodReadback)
	member.Readback = recorded(t, "reports these seeded files absent: roles/dev.md — the briefing did not reach it")

	detail, report := a.readbackOne(context.Background(), &strings.Builder{}, campaign, member)
	if detail != "" {
		t.Fatalf("a good second answer still failed: %s", detail)
	}
	if report.Dispatch != "d002" {
		t.Errorf("the new answer must be recorded against d002; got %q", report.Dispatch)
	}
	ask, err := os.ReadFile(filepath.Join(state, "input", "d002.md"))
	if err != nil {
		t.Fatalf("the member was not asked again: %v", err)
	}
	for _, want := range []string{
		"This is dispatch d002: confirm your briefing",
		"`create` was resumed after an earlier attempt failed.",
		"Your answer to d001 could not be used: reports these seeded files absent",
		"Nothing in your briefing has changed since.",
	} {
		if !strings.Contains(string(ask), want) {
			t.Errorf("the second asking must carry %q:\n%s", want, ask)
		}
	}
	if strings.Contains(string(ask), "dispatch d001:") {
		t.Errorf("the second asking is headed with the first one's id:\n%s", ask)
	}
}

// A readback that passed against files the operator has since changed is
// asked for again, naming the files.
func TestAResumedCreateAsksAgainWhenTheBriefingChanged(t *testing.T) {
	a, state := readbackMemberApp(t, "unused", goodReadback)
	campaign, member := readbackCampaign()
	earlierReadback(t, state, true, goodReadback)
	member.Readback = recorded(t, "")
	member.SeededInputs = map[string]string{"roles/dev.md": "rewritten brief"}

	if detail, _ := a.readbackOne(context.Background(), &strings.Builder{}, campaign, member); detail != "" {
		t.Fatalf("a good answer to the changed briefing failed: %s", detail)
	}
	ask, err := os.ReadFile(filepath.Join(state, "input", "d002.md"))
	if err != nil {
		t.Fatalf("a member whose briefing changed was not asked again: %v", err)
	}
	for _, want := range []string{"Your answer to d001 was accepted.", "changed since, so read them again: roles/dev.md."} {
		if !strings.Contains(string(ask), want) {
			t.Errorf("the second asking must carry %q:\n%s", want, ask)
		}
	}
}

// A readback nobody answered is still open, so the resumed create continues
// it, and says so, rather than presenting it as a new dispatch.
func TestAResumedCreateContinuesAnUnansweredReadback(t *testing.T) {
	a, state := readbackMemberApp(t, goodReadback)
	campaign, member := readbackCampaign()
	earlierReadback(t, state, false, "")

	detail, report := a.readbackOne(context.Background(), &strings.Builder{}, campaign, member)
	if detail != "" {
		t.Fatalf("the answer to the continued readback failed: %s", detail)
	}
	if report.Dispatch != "d001" {
		t.Errorf("the answer closed d001; got %q", report.Dispatch)
	}
	ask, err := os.ReadFile(filepath.Join(state, "input", "d001.001.md"))
	if err != nil {
		t.Fatalf("the open readback was not continued: %v", err)
	}
	for _, want := range []string{"This is dispatch d001: confirm your briefing", "This dispatch, d001, is still open"} {
		if !strings.Contains(string(ask), want) {
			t.Errorf("the continuation must carry %q:\n%s", want, ask)
		}
	}
}

// A first create asks exactly as it always has: d001, and no word of a resume.
func TestAFirstReadbackIsAskedAsBefore(t *testing.T) {
	_, member := readbackCampaign()
	prompt := readbackPrompt(member, "d001", "")
	if !strings.HasPrefix(prompt, "This is dispatch d001: confirm your briefing before any work is assigned.\n\n1. Run:") {
		t.Errorf("the first asking changed:\n%s", prompt)
	}
	if strings.Contains(prompt, "resumed") {
		t.Errorf("a first asking must not mention a resume:\n%s", prompt)
	}
}
