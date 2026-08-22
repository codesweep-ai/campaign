# Mission

Add a `--dry-run` flag to the report generator, and prove it writes nothing.

## Acceptance

1. `report --dry-run` exits 0 and creates no file.
2. A test fails without the flag's implementation and passes with it.
3. The flag is documented where the other flags are.
