package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/codesweep-ai/campaign/internal/model"
	"github.com/codesweep-ai/campaign/internal/store"
)

// A record written before attempts were kept is resumed without inventing
// the first attempt's deadline: its start is createdAt, and its deadline is
// unknown.
func TestAResumeOfAnOlderRecordKeepsItsFirstStartOnly(t *testing.T) {
	a := &app{store: store.Store{Dir: t.TempDir()}}
	created := time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC)
	old := &model.Campaign{Name: "old", Provisioning: "create-failed", CreatedAt: created, Deadline: created.Add(time.Hour)}
	if err := a.store.Save(old); err != nil {
		t.Fatal(err)
	}
	now := created.Add(10 * time.Minute)
	planned := &model.Campaign{Name: "old", CreatedAt: now, Deadline: now.Add(time.Hour)}
	adopted, err := a.adoptResumableCreate(planned)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.CreateAttempt{{StartedAt: created}, {StartedAt: now, Deadline: now.Add(time.Hour)}}
	if len(adopted.Attempts) != 2 || adopted.Attempts[0] != want[0] || adopted.Attempts[1] != want[1] {
		t.Fatalf("attempts = %+v, want %+v", adopted.Attempts, want)
	}
	var shown strings.Builder
	printDeadline(&shown, liveDeadline(adopted, now))
	if strings.Contains(shown.String(), "replaces") {
		t.Errorf("an unknown earlier deadline must not be shown as replaced:\n%s", shown.String())
	}
}

// Without a declared deadline observe says there is none, and ls shows a dash.
func TestNoDeclaredDeadlineIsSaid(t *testing.T) {
	c := &model.Campaign{Name: "open", CreatedAt: time.Now(), Attempts: []model.CreateAttempt{{StartedAt: time.Now()}}}
	if d := liveDeadline(c, time.Now()); d != nil {
		t.Fatalf("no deadline was declared, got %+v", d)
	}
	var shown strings.Builder
	printDeadline(&shown, nil)
	if !strings.HasPrefix(shown.String(), "DEADLINE — none") {
		t.Errorf("observe must say no deadline was declared:\n%s", shown.String())
	}
}

// After a resume, ls and observe name the attempt that set the live deadline,
// and observe lists the deadline it replaced.
func TestAMovedDeadlineNamesTheAttemptThatSetIt(t *testing.T) {
	first := time.Date(2026, 9, 22, 14, 0, 0, 0, time.UTC)
	second := first.Add(20 * time.Minute)
	c := model.Campaign{Name: "moved", CreatedAt: first, Deadline: second.Add(time.Hour),
		Attempts: []model.CreateAttempt{
			{StartedAt: first, Deadline: first.Add(time.Hour)},
			{StartedAt: second, Deadline: second.Add(time.Hour)},
		}}
	a := &app{store: store.Store{Dir: t.TempDir()}}
	if err := a.store.Save(&c); err != nil {
		t.Fatal(err)
	}
	var ls strings.Builder
	cmd := a.lsCmd()
	cmd.SetOut(&ls)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ls.String(), "2026-09-22T15:20:00Z (attempt 2)") {
		t.Errorf("ls must name the attempt that set the deadline:\n%s", ls.String())
	}
	var shown strings.Builder
	printDeadline(&shown, liveDeadline(&c, second.Add(10*time.Minute)))
	want := "DEADLINE — 2026-09-22T15:20:00Z, 50m0s from now\n" +
		"  moved by create attempt 2, which started at 2026-09-22T14:20:00Z. A deadline read before then is out of date.\n" +
		"  replaces 2026-09-22T15:00:00Z\n\n"
	if shown.String() != want {
		t.Errorf("observe shows:\n%s\nwant:\n%s", shown.String(), want)
	}
}
