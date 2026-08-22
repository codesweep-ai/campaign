package cli

// The readback's question and its mechanical verification. It asks for a
// RESTATEMENT, never a confirmation: a member that read nothing will happily
// answer {"read": true}; it cannot invent the scope its brief describes.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/codesweep-ai/campaign/internal/model"
)

type readbackReport = model.Readback

// readbackPrompt is dispatch d001, every member, every campaign. The answer
// travels as an ordinary reply — the member's first use of the one verb its
// whole campaign depends on, which is itself part of the check.
func readbackPrompt(member model.Member) string {
	return fmt.Sprintf(`This is dispatch d001: confirm your briefing before any work is assigned.

1. Run: cs-campaign-member check-inputs
   It prints any briefing file you were promised that is absent.
2. Read ~/%s, and then every file it lists under "inputs", in order.
3. Write a file (e.g. /tmp/readback.json) holding EXACTLY this JSON, on a single line:

{"member":"%s","role":"%s","branch":"<your branch, from member.json>","missing":[<output of check-inputs, as strings, else empty>],"goal":"<one sentence, in your own words: what this campaign is asking you to accomplish>","scope":"<one sentence: what you own and what you must not touch>","obligations":"<one sentence: what this campaign requires of you, and what happens to your work if you do not do it>"}

4. Reply with it: cs-campaign-member reply --file /tmp/readback.json

Do not perform any of the work yet. Your reply closes this dispatch.`,
		guestMemberJSON, member.Name, member.Role)
}

// parseReadback extracts the restatement from a reply's note. Tolerant of a
// model that wrapped the JSON in prose or a code fence: the outermost braces
// are the object.
func parseReadback(note string) (readbackReport, error) {
	var r readbackReport
	body := strings.TrimSpace(note)
	if i := strings.Index(body, "{"); i > 0 {
		body = body[i:]
	}
	if j := strings.LastIndex(body, "}"); j >= 0 {
		body = body[:j+1]
	}
	if !strings.HasPrefix(body, "{") {
		return r, errors.New("the reply carries no readback JSON object")
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		return r, fmt.Errorf("readback is not valid JSON (%v)", err)
	}
	return r, nil
}

// verifyReadback checks STRUCTURE and never content: everything here is a
// comparison against a value the host already knows, or a presence check.
// Whether "backend" correctly describes the backend is the operator's call.
func verifyReadback(member model.Member, r readbackReport, briefed bool) string {
	if r.Member != "" && r.Member != member.Name {
		return fmt.Sprintf("answered as %q — this member is %q; the fleet may be misaddressed", r.Member, member.Name)
	}
	if len(r.Missing) > 0 {
		return fmt.Sprintf("reports these seeded files absent: %s — the briefing did not reach it", strings.Join(r.Missing, ", "))
	}
	if member.Branch != "" && r.Branch != "" && r.Branch != member.Branch {
		return fmt.Sprintf("believes its branch is %q; it is %q — work committed there would not be fetched", r.Branch, member.Branch)
	}
	// Required of every member, briefed or not: the orientation exists either
	// way, and it carries the obligation whose failure is irreversible.
	if strings.TrimSpace(r.Obligations) == "" {
		return "could not state what the campaign requires of it — it has not read its orientation"
	}
	if !briefed {
		return ""
	}
	if strings.TrimSpace(r.Goal) == "" {
		return "could not state what the campaign is asking of it"
	}
	if strings.TrimSpace(r.Scope) == "" {
		return "could not state what it owns"
	}
	return ""
}

// oneLine collapses a restatement onto a single row without removing any of
// it — clipping is what this replaced.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
