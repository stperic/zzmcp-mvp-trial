# zzmcp MVP trial

This isolated checkout starts from zzmcp commit `29657f465269b00b3a246436acadedd9e5882b0b`. Its source has not been modified to plant a defect. `trace.yaml` adopts the 32 requirements and their links from zztrace's `reports/acceptance/2026-10-17-trace-mvp-agent/requirements.yaml`, using `zztrace-trace migrate`. IDs and requirement text are unchanged. The hazard description comes from that file's comment. The original pilot used an owner proxy, so the format's inherited accepted status records the earlier proxy review, not human acceptance.

The source and its old authority database stay in the original sibling checkout. This trial uses one canonical Git graph and a fresh authority for evidence. Missing verification links remain visible.

## Planned changes

1. Bug fix, R-RES-3: prove that a typed nil result can invoke `Summary()` before object validation, then ensure invalid output returns `INVALID_RESULT` without invoking a summary. Preserve valid typed summaries and schema-tool behavior.
2. Feature, R-RES-2: propose extending the existing `Summarizer` interface to `AddSchemaTool` outputs. Review the requirement revision before accepting it. JSON fallback, structured output and invalid-result handling must remain consistent across both registration paths.

The work is a proposal until the owner reviews the exact pull requests. The graph policy requires the `stperic` owner's forge review of result changes, and freezes R-SRV-10 credential isolation. Agent self-review is recorded as such, not as owner approval.

## Evidence

The trial runner stores raw test output and authority files outside tracked source. Committed graphs, reports and tagged baselines are captured with the current zztrace CLI. Each baseline preserves remaining gaps and its policy history. The first tag is only the history boundary, not a claim about earlier development.

## Bug proposal: R-RES-3

The existing requirement already requires `INVALID_RESULT` for null output. Its implementation links originally named only `ObjectResult`; the failing path begins in `AddTool` and goes through `addTool`. Those links and a regression test are added. Working scope includes R-RES-3, R-RES-2 and R-TOOL-1 because the shared registration callback and summary behavior are affected. No change to credential-isolation code is needed.

The regression returns a nil `*greetOutput`, which implements `Summarizer` through its value-receiver method. The original code invokes that method before validating the null output, causing a panic. The proposed fix validates the result first, preserving successful typed summaries and schema-tool JSON text.
