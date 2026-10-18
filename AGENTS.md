# zzmcp agent instructions

zzmcp is a small Go module of shared MCP conventions over the official MCP Go SDK: one error shape, schema-validated tools, structured object results and serving helpers. See [README.md](README.md) for scope and behavior. Its consumers are zztrace today and zzrouter, AlphaDB and zzfuse later; keep the API small and their needs in mind.

## Build and verify

- Go 1.25 or later. Run `gofmt -l .`, `go vet ./...` and `go test -race ./...` before reporting a change complete.
- Pair each behavior change with tests for the success path and the failure it prevents.

## Requirements and traceability

`trace.yaml` is the canonical Git graph, adopted from the earlier zzmcp pilot without changing its requirement IDs. That pilot used an owner proxy, not human acceptance. `trace.lock` records that links were checked; it is not an approval. Test results and baselines live in the separate zztrace authority.

- Before editing, run `zztrace-trace find WORDS`, `scope ID...`, and `code PATH#SYMBOL` as needed. Change code within the declared requirement scope; explain necessary shared-helper or test edits before making them.
- Run `zztrace-trace policy --base BASE --scope ID,ID` before editing and after the change. Missing owner approval remains a merge gate; a frozen decision stops the edit.
- Propose changed requirement text in Git before implementing new behavior. State the reason and preserve the previous text in the review packet. Do not accept your own proposal or claim human review.
- Keep code and test links current. Run `impact --base BASE --scope ID,ID`, execute every suggested test command, and also run the full checks above.
- Recheck each changed link before `stamp ID...`; run `check` before committing. Never restamp a risk control just to silence a finding.
- Commit before `zztrace graph import` and `graph record`. Recorded tests remain `local_assertion` evidence. Actual forge reviews must be read by `policy --github OWNER/REPO --pr N`; do not supply manual approval flags.

See `MVP-TRIAL.md` for the baseline, proposed tasks and provenance. The original sibling checkout and its historical authority remain unchanged during this trial.
