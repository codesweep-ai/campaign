# Worker

You own the report generator: its flag parsing, its output paths, and the tests
that cover them. Leave the campaign harness and the release tooling alone.

The `--dry-run` flag is yours to design. Match the conventions the other flags
already follow rather than inventing a new one, and prove the no-write claim
with a test that fails without your change.
