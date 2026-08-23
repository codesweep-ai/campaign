# Working in this repo

This file routes; it holds no knowledge of its own. The docs it points at are authoritative. When
one covers what you need, open it rather than inferring the answer from the code. When nothing
covers it, say so instead of guessing.

Two jobs bring you here, and they read different pages. **Running a campaign** means driving the
harness: start at the manual. **Changing the harness** means editing Go under `internal/` and
`cmd/`: start at the contributing guide.

- [README.md](README.md) · what this is, and how to run it.
- [INSTALL.md](INSTALL.md) · how to get the tools, and the setup they need once.
- [MANUAL.md](MANUAL.md) · the full surface, for someone using the tools. Its **Notes for agents**
  section is written for you.
- [PROTOCOL.md](PROTOCOL.md) · the dispatch protocol, stated for any implementation of it.
- [CONTRIBUTING.md](CONTRIBUTING.md) · conventions, and the rituals a diff does not show. Read it
  before your first change.
- [SPEC.md](SPEC.md) · what the behaviour must be, and what is left open.
- [ledger/AGENTS.md](ledger/AGENTS.md) · this repo keeps a ledger of open issues. Read it before
  you start work.

`cs-campaign --help` is generated from the code and is always current. Prefer it over any command
line quoted in a document.
