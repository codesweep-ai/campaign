package cli

// The readback is dispatch #1 on every node. It is not a liveness check: it
// is the only evidence a member UNDERSTOOD its briefing — knows who it is,
// read its orientation, can state its job — and every check made against the
// answer is mechanical (a comparison against a value the host already knows,
// or a presence check). A member that read nothing will happily confirm; it
// cannot invent the scope its brief describes, which is why the fields are
// prose.
//
// Host-driven ON PURPOSE, the one create-time host→agent exception: a dead
// orchestrator cannot report its own death, and at create there is no
// orchestrator to delegate to yet. The answer arrives as an ordinary reply —
// no transcript scraping, no fences, no soft-deadline dance: reply existence
// is awaited by the same ladder that recovers any stopped node.

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
)

// readbackBound caps one member's readback end to end. Far above any healthy
// figure on purpose: the cost of waiting is a slow pre-flight, the cost of
// failing early is aborting a campaign whose member was merely slow — and the
// ladder inside the bound is what actually recovers a stopped one.
var readbackBound = 15 * time.Minute

// runReadback asks every member for its readback concurrently, awaits the
// replies (ladder on silence), verifies each restatement mechanically, and
// records it on the campaign. A failure means "do not dispatch" and fails
// create by name.
func (a *app) runReadback(ctx context.Context, out io.Writer, campaign *model.Campaign) error {
	type result struct {
		name, cli, detail string
		report            model.Readback
	}
	results := make([]result, len(campaign.Members))
	var mu sync.Mutex // serialises writes to out
	var wg sync.WaitGroup
	for i := range campaign.Members {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			member := campaign.Members[i]
			detail, report := a.readbackOne(ctx, &lockedWriter{w: out, mu: &mu}, campaign, member)
			results[i] = result{member.Name, member.CLI, detail, report}
		}(i)
	}
	wg.Wait()

	// Record the restatements whether they passed or failed — a rejected one
	// is exactly the answer an operator needs to re-read afterwards.
	for i := range results {
		if results[i].report.Empty() {
			continue
		}
		report := results[i].report
		report.Detail = results[i].detail
		campaign.Members[i].Readback = &report
	}
	_ = a.store.Save(campaign)

	var dead []string
	for _, r := range results {
		if r.detail != "" {
			fmt.Fprintf(out, "BAD readback %s (%s): %s\n", r.name, r.cli, r.detail)
			dead = append(dead, fmt.Sprintf("%s (%s): %s", r.name, r.cli, r.detail))
			continue
		}
		fmt.Fprintf(out, "ok  readback %s (%s) read its briefing%s\n", r.name, r.cli, declaredSuffixByName(campaign, r.name))
		// The restatement is PRINTED, never graded: only the operator knows
		// whether "backend" correctly describes the backend. Printed in full —
		// what a restatement puts last is the qualifying half.
		if r.report.Goal != "" {
			fmt.Fprintf(out, "      goal:  %s\n", oneLine(r.report.Goal))
		}
		if r.report.Scope != "" {
			fmt.Fprintf(out, "      scope: %s\n", oneLine(r.report.Scope))
		}
		if r.report.Obligations != "" {
			fmt.Fprintf(out, "      owes:  %s\n", oneLine(r.report.Obligations))
		}
	}
	if len(dead) > 0 {
		sort.Strings(dead)
		return fmt.Errorf("readback FAILED — %d member(s) could not confirm their briefing; do not dispatch:\n  %s",
			len(dead), strings.Join(dead, "\n  "))
	}
	return nil
}

// readbackOne asks one member for its readback and awaits the reply. Returns
// "" on success, else the reason it failed.
//
// A readback whose FORM is wrong is asked for once more. The reply closed its
// dispatch, so the second asking is a new dispatch, which is what rework is
// (PROTOCOL.md §3), and the host is the dispatcher here. A model that drops a
// brace has not misread its briefing, and failing the create for it throws
// away a team that is up with its keys lent. What a second try cannot cure is
// never retried: a member that answers as someone else, reports its seeded
// files absent or names the wrong branch has told the host something true.
//
// On a resumed create a member that already passed is not asked again, and one
// that is asked again is told why. Re-sending the first asking word for word,
// headed with another dispatch's id, taught members the channel was broken:
// seen live, three of them started work under a dispatch that said not to.
func (a *app) readbackOne(ctx context.Context, out io.Writer, campaign *model.Campaign, member model.Member) (string, model.Readback) {
	briefed := len(member.SeededInputs) > 0
	facts, failed := a.sandbox.probeMember(ctx, member)
	if failed {
		return fmt.Sprintf("dispatch failed: cannot reach %s to deliver", member.Name), model.Readback{}
	}
	if kept, ok := keptReadback(member, facts); ok {
		fmt.Fprintf(out, "…  %s: confirmed its briefing in %s on an earlier create, and the briefing is unchanged — not asked again\n", member.Name, kept.Dispatch)
		return "", kept
	}
	id, _, err := protocol.SendTarget(facts)
	if err != nil {
		return "dispatch failed: " + err.Error(), model.Readback{}
	}
	prompt := readbackPrompt(member, id, resumedReason(member, facts))
	for attempt := 1; ; attempt++ {
		if attempt == 1 {
			_, _, err = a.hostSendPrepared(ctx, member, facts, prompt, false)
		} else {
			id, _, err = a.hostSend(ctx, member, prompt, false)
		}
		if err != nil {
			return "dispatch failed: " + err.Error(), model.Readback{}
		}
		reply, err := a.awaitReply(ctx, out, member, id, campaign.Policy, readbackBound)
		if err != nil {
			return err.Error(), model.Readback{}
		}
		report, perr := parseReadback(reply.Note)
		report.At, report.Dispatch, report.Inputs = time.Now().UTC(), id, maps.Clone(member.SeededInputs)
		var detail string
		switch {
		case perr != nil:
			// The member answered — it is alive; it just did not comply. A
			// different diagnosis from a dead member.
			detail = "replied but " + perr.Error() + ": " + truncate(reply.Note, 300)
		default:
			detail = verifyReadback(member, report, briefed)
		}
		if detail == "" {
			// The turn that just answered is the only place a model declaration
			// can be checked, so the assertion rides the turn the readback spent.
			return a.assertDeclaredTurnConfig(ctx, member), report
		}
		formOnly := perr != nil || readbackIncomplete(report, briefed)
		if !formOnly {
			return detail, report
		}
		if attempt == 2 {
			return detail + " — asked twice, and wrong both times", report
		}
		fmt.Fprintf(out, "…  %s: its readback could not be used (%s) — asked once more\n", member.Name, truncate(detail, 160))
		prompt = readbackReworkPrompt(member, detail)
	}
}

// readbackIncomplete says a readback parsed and left a required sentence out:
// a fault of form, which asking again can cure.
func readbackIncomplete(r readbackReport, briefed bool) bool {
	if strings.TrimSpace(r.Obligations) == "" {
		return true
	}
	return briefed && (strings.TrimSpace(r.Goal) == "" || strings.TrimSpace(r.Scope) == "")
}

// keptReadback returns the readback an earlier create recorded when the member
// has nothing left to confirm: the answer passed, it closed the member's
// latest dispatch, and the seeded files are the ones it answered against.
func keptReadback(member model.Member, facts protocol.Facts) (model.Readback, bool) {
	r := member.Readback
	if r == nil || r.Empty() || r.Detail != "" || r.Dispatch == "" {
		return model.Readback{}, false
	}
	d := protocol.Current(facts.Msgs)
	if d == nil || d.ID != r.Dispatch || !facts.Replies[d.ID] || len(changedInputs(r.Inputs, member.SeededInputs)) > 0 {
		return model.Readback{}, false
	}
	return *r, true
}

// resumedReason says why a member is asked for its readback again, or "" when
// this is the first asking. Before the mission opens, every dispatch a member
// holds is a readback, so holding any means an earlier create already asked.
func resumedReason(member model.Member, facts protocol.Facts) string {
	d := protocol.Current(facts.Msgs)
	if d == nil {
		return ""
	}
	why := "`create` was resumed after an earlier attempt failed. "
	if !facts.Replies[d.ID] {
		return why + fmt.Sprintf("This dispatch, %s, is still open, because no reply to it exists yet. Answer it once, as below.", d.ID)
	}
	r := member.Readback
	if r == nil || r.Dispatch != d.ID {
		return why + fmt.Sprintf("The host kept no record of your answer to %s, so it asks once more.", d.ID)
	}
	if r.Detail != "" {
		why += fmt.Sprintf("Your answer to %s could not be used: %s. ", d.ID, r.Detail)
	} else {
		why += fmt.Sprintf("Your answer to %s was accepted. ", d.ID)
	}
	if changed := changedInputs(r.Inputs, member.SeededInputs); len(changed) > 0 {
		return why + "These seeded files have changed since, so read them again: " + strings.Join(changed, ", ") + "."
	}
	return why + "Nothing in your briefing has changed since."
}

// changedInputs names the seeded files whose digest differs between two
// recordings, including any present in only one of them.
func changedInputs(before, now map[string]string) []string {
	var changed []string
	for name, digest := range now {
		if before[name] != digest {
			changed = append(changed, name)
		}
	}
	for name := range before {
		if _, ok := now[name]; !ok {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return changed
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func declaredSuffixByName(campaign *model.Campaign, name string) string {
	for _, m := range campaign.Members {
		if m.Name == name {
			return declaredSuffix(m)
		}
	}
	return ""
}

// assertDeclaredTurnConfig closes the loop a declared model opens: the
// declaration is only worth something if something checks it took. A
// silently-failed pin — the member answering on the CLI's default — is
// invisible until the bill otherwise.
func (a *app) assertDeclaredTurnConfig(ctx context.Context, member model.Member) string {
	if member.Model == "" && member.Effort == "" {
		return ""
	}
	models, efforts, supported, err := a.sandbox.observedTurnConfig(ctx, member)
	if !supported {
		return ""
	}
	if err != nil {
		return "declared " + declaredSummary(member) + " but the turn's transcript could not be read: " + err.Error()
	}
	if member.Model != "" {
		if detail := matches("model", member.Model, models); detail != "" {
			return detail
		}
	}
	if member.Effort != "" && turnEffortReadable(member.CLI) {
		if detail := matches("effort", member.Effort, efforts); detail != "" {
			return detail
		}
	}
	return ""
}

// matches reports why observed does not carry want, or "" when it does. A CLI
// may name several models in one transcript (a subagent, a summariser), so
// the declared one has to be present rather than alone.
func matches(field, want string, observed []string) string {
	if len(observed) == 0 {
		return fmt.Sprintf("declared %s %s but the turn's transcript names no %s, so the declaration cannot be confirmed", field, want, field)
	}
	if slices.Contains(observed, want) {
		return ""
	}
	return fmt.Sprintf("declared %s %s but the turn answered on %s — the declaration did not take", field, want, strings.Join(observed, ", "))
}

func declaredSummary(member model.Member) string {
	var parts []string
	if member.Model != "" {
		parts = append(parts, "model "+member.Model)
	}
	if member.Effort != "" {
		parts = append(parts, "effort "+member.Effort)
	}
	return strings.Join(parts, ", ")
}

// declaredSuffix names what was declared and says plainly whether the turn
// confirmed it — an adapter whose transcript cannot be read must not print
// the same line as one that was checked.
func declaredSuffix(member model.Member) string {
	summary := declaredSummary(member)
	if summary == "" {
		return ""
	}
	if !turnConfigReadable(member.CLI) {
		return " on " + summary + " (declared — this adapter's turn cannot be read back to confirm it)"
	}
	if member.Effort != "" && !turnEffortReadable(member.CLI) {
		return " on " + summary + " (model confirmed by the answering turn; this adapter does not name its effort)"
	}
	return " on " + summary + " (confirmed by the answering turn)"
}
