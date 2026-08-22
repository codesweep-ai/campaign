package protocol

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Reply is the artifact that closes a dispatch. Presence is the signal —
// "did it come back?" is a file-existence check with no parsing — and the
// content is co-authored: the model supplies Note (and Outcome on the
// mission); the tool stamps Dispatch, At and the branch evidence, so a claim
// of delivered work arrives beside the measurement that checks it.
type Reply struct {
	Dispatch string      `json:"dispatch"`
	Phase    string      `json:"phase"` // "done" | "needs-input"
	Note     string      `json:"note"`
	Outcome  string      `json:"outcome,omitempty"` // mission reply only
	Unmet    []string    `json:"unmet,omitempty"`   // mission reply only
	At       time.Time   `json:"at"`
	Repos    []RepoState `json:"repos,omitempty"`
}

// RepoState is the tool-stamped evidence for one repository at reply time.
type RepoState struct {
	Repo string `json:"repo"`
	Head string `json:"head,omitempty"`
	// TreeDiffersFromBase is the durable-work test: tree against base, never
	// commit count (an empty commit is not delivered work).
	TreeDiffersFromBase bool   `json:"treeDiffersFromBase"`
	Dirty               bool   `json:"dirty,omitempty"` // uncommitted changes present
	Error               string `json:"error,omitempty"`
}

// Outcomes is the mission vocabulary. Exactly one applies, and the test for
// campaign-met is mechanical: if anything is unmet, it is not campaign-met.
var Outcomes = []string{"campaign-met", "campaign-converged", "campaign-exhausted", "campaign-blocked"}

// ValidateMissionReply enforces the vocabulary in code rather than prose.
func ValidateMissionReply(r Reply) error {
	ok := slices.Contains(Outcomes, r.Outcome)
	if !ok {
		return fmt.Errorf("mission reply needs --outcome, exactly one of: %s", strings.Join(Outcomes, ", "))
	}
	empty := true
	for _, u := range r.Unmet {
		if strings.TrimSpace(u) != "" {
			empty = false
			break
		}
	}
	if r.Outcome == "campaign-met" && !empty {
		return errors.New("campaign-met names nothing unmet — if you can name something unmet, it is not campaign-met")
	}
	if r.Outcome != "campaign-met" && empty {
		return fmt.Errorf("%s must name what remains unmet (--unmet, repeatable): the list is the payload, the label only summarises it", r.Outcome)
	}
	return nil
}

// ReplyPath is the reply file for one dispatch, relative to $HOME.
func ReplyPath(id string) string { return RepliesDir + "/" + id + ".json" }

// WriteReplyLocal writes a reply into this machine's own output channel.
// Written to a temp name and renamed so the file appears whole — presence is
// the signal, so a torn write must never be observable.
func WriteReplyLocal(home string, r Reply) error {
	dir := filepath.Join(home, RepliesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+r.Dispatch+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, r.Dispatch+".json"))
}

// ParseReply decodes a reply file's bytes.
func ParseReply(b []byte) (Reply, error) {
	var r Reply
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("reply is not valid JSON: %w", err)
	}
	return r, nil
}

// Entry is one line of the orchestrator's append-only log. A later entry of a
// kind supersedes an earlier one; how the log is read is the reader's choice.
type Entry struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // "plan" | "accepted" | "assessment"
	Text string    `json:"text"`
}

// LogKinds are the only kinds the log accepts.
var LogKinds = map[string]bool{"plan": true, "accepted": true, "assessment": true}

// AppendLogLocal appends one entry to this machine's own log. O_APPEND on one
// line: a careless append is additive, and there is no rewrite path at all.
func AppendLogLocal(home string, e Entry) error {
	if !LogKinds[e.Kind] {
		return fmt.Errorf("log kind %q is not one of plan, accepted, assessment", e.Kind)
	}
	if err := os.MkdirAll(filepath.Join(home, OutputDir), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(home, LogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// ParseLog reads log bytes, skipping a torn final line rather than failing
// the read.
func ParseLog(b []byte) []Entry {
	var out []Entry
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if json.Unmarshal([]byte(line), &e) == nil && LogKinds[e.Kind] {
			out = append(out, e)
		}
	}
	return out
}

// AcceptedFor is the acceptance record for ONE node: the one input to node
// state that does not come from the node itself. Entries are node-qualified
// ("backend/d002") because dispatch IDs are per-node sequences — a bare
// "d002" exists on every agent, and a global set silently freed a different
// node's unjudged reply (adversarial review, finding 2). A bare legacy entry
// matches no node.
func AcceptedFor(entries []Entry, node string) map[string]bool {
	out := map[string]bool{}
	prefix := node + "/"
	for _, e := range entries {
		if e.Kind != "accepted" {
			continue
		}
		if text := strings.TrimSpace(e.Text); strings.HasPrefix(text, prefix) {
			out[strings.TrimPrefix(text, prefix)] = true
		}
	}
	return out
}

// AcceptanceText renders the node-qualified acceptance entry for a dispatch.
func AcceptanceText(node, id string) string { return node + "/" + id }

// PutFileScript is the shell fragment that materialises one file on a node
// from base64 — the payload never crosses as shell-visible text, so it cannot
// break quoting or execute. path is $HOME-relative.
func PutFileScript(path, content string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(content))
	dir := filepath.Dir(path)
	return fmt.Sprintf(`mkdir -p ~/%s && printf %%s %s | base64 -d > ~/%s && chmod 600 ~/%s`, dir, enc, path, path)
}

// PutMsgScript is PutFileScript for dispatch messages: it refuses to clobber
// an existing name. A message name, once minted, is immutable — and the mint
// (NextDispatchID/NextMsgName from a listing) and the write are two round
// trips, so two sends fired concurrently by the same dispatcher can race the
// same name (the ID-mint TOCTOU; adversarial review, low-severity note). The
// noclobber creation is the atomic claim on the name; the loser fails with
// the marker IsDeliveryCollision recognises and re-probes — the winner's
// message is in the listing now, so the reclassification is ordinary. The
// exposed surface is the orchestrator's guest `send` (a model can emit two
// tool calls in parallel); host sends are operator-typed, serial by
// construction, and keep PutFileScript.
func PutMsgScript(path, content string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(content))
	dir := filepath.Dir(path)
	// `true >` rather than `: >` — a redirection error on a special builtin
	// like `:` aborts a POSIX shell outright, skipping the else branch.
	return fmt.Sprintf(`mkdir -p ~/%s && if { set -C; true > ~/%s; } 2>/dev/null; then printf %%s %s | base64 -d >| ~/%s && chmod 600 ~/%s; else echo %s ~/%s; exit 1; fi`,
		dir, path, enc, path, path, deliveryCollisionMarker, path)
}

// deliveryCollisionMarker is what PutMsgScript prints when the target name
// already exists. Chosen to collide with nothing a transport prints.
const deliveryCollisionMarker = "CS_MSG_EXISTS"

// IsDeliveryCollision reports whether a failed PutMsgScript run failed
// because the message name already existed on the node.
func IsDeliveryCollision(out []byte) bool {
	return strings.Contains(string(out), deliveryCollisionMarker)
}

// Trigger is the product-authored text that starts a turn. It carries the one
// obligation now due — reply before you stop — because tool output is read
// every turn while the orientation is read once: the correction arrives
// in-band, where the model is looking.
func Trigger(msgPath, id string, fresh bool) string {
	if fresh {
		return fmt.Sprintf("Read ~/%s and then every file it lists under \"inputs\" — that is your standing context for this campaign. "+
			"Then read ~/%s and follow it. Dispatch ID: %s. "+
			"When the work is concluded, run `cs-campaign-member reply --file <path-to-your-summary>`. "+
			"Ending your turn without a reply reads as stopped, and you will be continued.",
			MemberDoc, msgPath, id)
	}
	return fmt.Sprintf("Read ~/%s and follow it. Dispatch ID: %s. "+
		"When the work is concluded, run `cs-campaign-member reply --file <path-to-your-summary>`. "+
		"Ending your turn without a reply reads as stopped, and you will be continued.",
		msgPath, id)
}

// ContinueBody is the templated continue for a node stopped without a reply —
// the mechanical layer's message, written by code, never by a model.
func ContinueBody(id string) string {
	return fmt.Sprintf("Dispatch %s is still open — no reply exists at ~/%s. "+
		"If the work is done, run `cs-campaign-member reply --file <path-to-your-summary>` now. "+
		"If it is not done, continue working, and reply when it is.", id, ReplyPath(id))
}

// RestartBody is the re-anchor a restarted session receives: mechanical
// replay, never a dispatcher-authored summary. The new session lost its
// memory, so it is pointed back at everything it already had.
func RestartBody(id string, msgNames []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Your session was restarted; your memory of this campaign is gone, but your machine and your work are intact.\n")
	fmt.Fprintf(&b, "Re-anchor: read ~/%s and every file it lists under \"inputs\". ", MemberDoc)
	fmt.Fprintf(&b, "Then read every message of your open dispatch %s, in this order:\n", id)
	for _, n := range msgNames {
		fmt.Fprintf(&b, "  ~/%s/%s\n", InputDir, n)
	}
	fmt.Fprintf(&b, "Check what already exists before redoing anything (your branch, ~/%s). ", OutputDir)
	fmt.Fprintf(&b, "When the work is concluded, run `cs-campaign-member reply --file <path-to-your-summary>`. "+
		"Ending your turn without a reply reads as stopped, and you will be continued.")
	return b.String()
}
