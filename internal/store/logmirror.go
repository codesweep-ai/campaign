package store

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// LogMirrorPath is the host's copy of one campaign's orchestrator log.
func (s Store) LogMirrorPath(name string) string {
	return filepath.Join(s.Dir, name+".log-mirror.jsonl")
}

// MergeLogMirror adds to the host's copy every complete line of log it does not
// already hold, in the order first seen, and never removes one. The copy is a
// backup of a claim (SPEC.md R24), and the log it backs up lives on a machine
// its member can write: an orchestrator that rewrites its log must not take
// the backup with it, which a copy replaced on every look did (SAC-102).
//
// Only lines ending in a newline count. The orchestrator may be appending as
// the log is read, and a half-written line copied now would stay beside its
// finished self forever. Several hosts' observes run at once, so the read and
// the append happen under one lock on the copy itself.
func (s Store) MergeLogMirror(name string, log []byte) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.LogMirrorPath(name), os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()
	held, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for line := range bytes.SplitSeq(held, []byte("\n")) {
		have[string(line)] = true
	}
	var add bytes.Buffer
	if len(held) > 0 && held[len(held)-1] != '\n' {
		add.WriteByte('\n')
	}
	complete := log
	if i := bytes.LastIndexByte(complete, '\n'); i >= 0 {
		complete = complete[:i]
	} else {
		complete = nil
	}
	for line := range bytes.SplitSeq(complete, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 || have[string(line)] {
			continue
		}
		have[string(line)] = true
		add.Write(line)
		add.WriteByte('\n')
	}
	if add.Len() == 0 || (add.Len() == 1 && add.Bytes()[0] == '\n') {
		return nil
	}
	_, err = f.Write(add.Bytes())
	return err
}

// ResetLogMirror removes the host's copy of a campaign's log. A new campaign
// starts its copy empty: one that only ever grows would otherwise carry the
// lines of an earlier campaign that had the same name.
func (s Store) ResetLogMirror(name string) error {
	err := os.Remove(s.LogMirrorPath(name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
