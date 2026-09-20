package cli

// The host's dispatcher machinery: probe, send, restart, and the mechanical
// recovery ladder — used against the orchestrator for the mission and against
// every member for the create-time readback (the one host→agent exception,
// made because a dead orchestrator cannot report its own death and at create
// there is no orchestrator to delegate to yet).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/protocol"
)

// hostSend delivers one message to one member and starts its turn: opens a
// new dispatch if the current one is closed, continues it if open. The
// classification is computed from the node's own channel, never chosen.
func (a *app) hostSend(ctx context.Context, member model.Member, body string, restart bool) (id string, opened bool, err error) {
	if strings.TrimSpace(body) == "" {
		return "", false, errors.New("an empty dispatch would spend the member's turn on nothing")
	}
	facts, failed := a.sandbox.probeMember(ctx, member)
	if failed {
		return "", false, fmt.Errorf("cannot reach %s to deliver", member.Name)
	}
	return a.hostSendPrepared(ctx, member, facts, body, restart)
}

// hostSendPrepared delivers against facts already probed, so a restart's
// kill/forget cannot race a second probe's view.
func (a *app) hostSendPrepared(ctx context.Context, member model.Member, facts protocol.Facts, body string, restart bool) (string, bool, error) {
	d := protocol.Current(facts.Msgs)
	var id string
	opened := false
	switch {
	case d == nil || facts.Replies[d.ID]:
		var err error
		if id, err = protocol.NextDispatchID(facts.Msgs); err != nil {
			return "", false, err
		}
		opened = true
	default:
		id = d.ID
	}
	return id, opened, a.deliver(ctx, member, facts, id, body, restart)
}

// openMission opens the reserved mission dispatch on the orchestrator. It
// refuses when any dispatch is open — at most one dispatch per node — and
// refuses a second mission outright.
func (a *app) openMission(ctx context.Context, member model.Member, body string) error {
	facts, failed := a.sandbox.probeMember(ctx, member)
	if failed {
		return errors.New("cannot reach the orchestrator to open the mission")
	}
	if d := protocol.Current(facts.Msgs); d != nil && !facts.Replies[d.ID] {
		return fmt.Errorf("the orchestrator has dispatch %s open; the mission cannot be opened over it", d.ID)
	}
	for _, m := range facts.Msgs {
		if m.ID == protocol.MissionID {
			return errors.New("this campaign's mission was already opened")
		}
	}
	return a.deliver(ctx, member, facts, protocol.MissionID, body, false)
}

// missionQuietBound is how long create waits for the orchestrator's readback
// turn to end before it opens the mission, and missionQuietPoll how often it
// looks. A healthy turn ends within seconds of writing its reply.
var (
	missionQuietBound = 2 * time.Minute
	missionQuietPoll  = 2 * time.Second
)

func missionQuietForTest(bound, poll time.Duration) func() {
	pb, pp := missionQuietBound, missionQuietPoll
	missionQuietBound, missionQuietPoll = bound, poll
	return func() { missionQuietBound, missionQuietPoll = pb, pp }
}

// openMissionWhenQuiet opens the mission once the orchestrator is in no turn.
//
// The readback's reply is an artifact, and writing it does not end the turn
// that wrote it. Opened at once, the mission lands in the inbox of a turn that
// is still running. A model that keeps going finds it there and does the whole
// mission inside the readback turn, and the turn started for the mission,
// queued behind that one, delivers its prompt after the mission is closed. With
// no mission in its inbox, the readback turn has nothing to carry on with, and
// ends. Past the bound the mission is opened anyway, as it always was.
func (a *app) openMissionWhenQuiet(ctx context.Context, out io.Writer, member model.Member, body string) error {
	deadline := time.Now().Add(missionQuietBound)
	for {
		facts, failed := a.sandbox.probeMember(ctx, member)
		if !failed && facts.Drivers == 0 && !strings.HasPrefix(facts.Agent, "busy") {
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(out, "…  %s is still in a turn after %s — opening the mission behind it\n", member.Name, missionQuietBound)
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(missionQuietPoll):
		}
	}
	return a.openMission(ctx, member, body)
}

func (a *app) deliver(ctx context.Context, member model.Member, facts protocol.Facts, id, body string, restart bool) error {
	return a.deliverNamed(ctx, member, protocol.NextMsgName(facts.Msgs, id, restart), id, body)
}

// hostResume carries an open dispatch on without spending a rung: the member's
// provider refused its last turn and the wait is over, or the turn never ran.
func (a *app) hostResume(ctx context.Context, member model.Member, facts protocol.Facts, id string, refused bool) error {
	return a.deliverNamed(ctx, member, protocol.NextResumeName(facts.Msgs, id), id, protocol.ResumeBody(id, refused))
}

func (a *app) deliverNamed(ctx context.Context, member model.Member, msgName, id, body string) error {
	msgPath := guestInputDir + "/" + msgName
	if err := a.sandbox.putMemberFile(ctx, member, msgPath, body); err != nil {
		return fmt.Errorf("deliver %s to %s: %w", msgName, member.Name, err)
	}
	return a.sandbox.startTurn(ctx, member, msgPath, id)
}

// hostRestart is rung two of the ladder, host-side: kill the wedged session,
// forget it, re-anchor a fresh one against the open dispatch by mechanical
// replay.
func (a *app) hostRestart(ctx context.Context, member model.Member) error {
	facts, failed := a.sandbox.probeMember(ctx, member)
	if failed {
		return fmt.Errorf("cannot reach %s — no recovery instrument reaches a machine that cannot be reached at all", member.Name)
	}
	d := protocol.Current(facts.Msgs)
	if d == nil || facts.Replies[d.ID] {
		return fmt.Errorf("%s has no open dispatch; a restart re-anchors an open dispatch, it does not assign work", member.Name)
	}
	_ = a.sandbox.killSession(ctx, member)
	a.sandbox.forgetSession(ctx, member)
	var mine []protocol.Msg
	for _, m := range facts.Msgs {
		if m.ID == d.ID {
			mine = append(mine, m)
		}
	}
	protocol.SortMsgs(mine)
	names := make([]string, 0, len(mine))
	for _, m := range mine {
		names = append(names, m.Name)
	}
	return a.deliver(ctx, member, facts, d.ID, protocol.RestartBody(d.ID, names), true)
}

// readReply fetches and parses one member's reply to dispatch id.
func (a *app) readReply(ctx context.Context, member model.Member, id string) (protocol.Reply, error) {
	out, err := a.sandbox.memberOutput(ctx, member, "cat ~/"+guestRepliesDir+"/"+id+".json")
	if err != nil {
		return protocol.Reply{}, fmt.Errorf("read %s's reply to %s: %w", member.Name, id, err)
	}
	return protocol.ParseReply(out)
}

// awaitReply runs the dispatcher loop for dispatch id on one member until its
// reply exists: compute, act mechanically (continue, then restart, under the
// campaign policy), recompute. Used by the create-time readback — the one place
// the host owns a whole dispatch lifecycle. bound caps the wait.
//
// id, rather than whatever dispatch the node is on now. Compute reports the
// node's CURRENT dispatch, and the two stop being the same the moment anything
// else opens one: a reply arrives for d002 while this call is still waiting on
// d001, and reading "the current dispatch's reply" hands back the wrong
// artifact. The readback then parsed a work summary as a briefing confirmation
// and failed the member for not being JSON — a member that had answered
// correctly, and whose d001 reply was sitting right there.
//
// The protocol makes this safe to check first: a dispatch is open until its
// reply appears, and a later dispatch can only have been opened because this
// one was closed. So a reply for id is the answer whenever it exists, whatever
// the node has moved on to since.
func (a *app) awaitReply(ctx context.Context, out io.Writer, member model.Member, id string, pol protocol.Policy, bound time.Duration) (protocol.Reply, error) {
	pol = pol.Resolve()
	deadline := time.Now().Add(bound)
	blind := 0
	refusalSaid := false // one line per refusal, not one per look
	for {
		facts, failed := a.sandbox.probeMember(ctx, member)
		if failed {
			blind++
		} else {
			blind = 0
		}
		if !failed && facts.Replies[id] {
			return a.readReply(ctx, member, id)
		}
		obs := protocol.Compute(facts, failed, protocol.Blind{Looks: blind}, map[string]bool{}, pol, time.Now().Unix())
		if obs.State != protocol.StateRefused {
			refusalSaid = false
		}
		switch obs.State {
		case protocol.StateReplied:
			// Some other dispatch's reply — ours is still outstanding, and the
			// check above is what ends this wait. Keep waiting.
		case protocol.StateStuck:
			return protocol.Reply{}, fmt.Errorf("%s is stuck (%s)", member.Name, obs.Detail)
		case protocol.StateRefused:
			// The provider refused the member's turn. Waiting is the move, and
			// the wait is said out loud: at create this used to look like a
			// member that would not answer its briefing.
			if obs.NextMove == "resume" {
				fmt.Fprintf(out, "…  %s: the wait its provider asked for is over — resumed, no rung spent (%s)\n", member.Name, obs.Detail)
				if err := a.hostResume(ctx, member, facts, obs.Dispatch, true); err != nil {
					fmt.Fprintf(out, "…  %s: resume failed and will be tried again: %v\n", member.Name, err)
				}
			} else if !refusalSaid {
				fmt.Fprintf(out, "…  %s: its provider refused the turn — waiting (%s)\n", member.Name, obs.Detail)
			}
			refusalSaid = true
		case protocol.StateStopped:
			switch obs.NextMove {
			case "resume":
				fmt.Fprintf(out, "…  %s: no turn ran for its last message — resumed, no rung spent\n", member.Name)
				if err := a.hostResume(ctx, member, facts, obs.Dispatch, false); err != nil {
					fmt.Fprintf(out, "…  %s: resume failed and will be tried again: %v\n", member.Name, err)
				}
			case "continue":
				fmt.Fprintf(out, "…  %s stopped without replying — continued (%s)\n", member.Name, obs.Detail)
				if _, _, err := a.hostSendPrepared(ctx, member, facts, protocol.ContinueBody(obs.Dispatch), false); err != nil {
					return protocol.Reply{}, err
				}
			case "restart":
				fmt.Fprintf(out, "…  %s did not answer continues — restarting its session\n", member.Name)
				if err := a.hostRestart(ctx, member); err != nil {
					return protocol.Reply{}, err
				}
			}
		}
		if time.Now().After(deadline) {
			return protocol.Reply{}, fmt.Errorf("%s did not reply within %s (last: %s %s)", member.Name, bound, obs.State, obs.Detail)
		}
		select {
		case <-ctx.Done():
			return protocol.Reply{}, ctx.Err()
		case <-time.After(protocol.PollInterval(pol)):
		}
	}
}
