# metaprompt

A CLI that improves an LLM prompt stored as a mustache template: read `name.mustache`, ask Claude —
through Anthropic's metaprompt — to rewrite it, write the result as `name.1.mustache`. The pipeline
is one straight line in `improve()` (`cmd/metaprompt/main.go`): read the file → `ParseTags` → a
four-step chain of `llm.Generate` calls → `Tags.ToMustache` → `Tags.Verify` → `revision.NextPath` →
write. There is no tool loop and no config file.

The four steps are `BuildAnalyze` → `BuildDraft` → `BuildRefine` → `BuildPolish`, each one a single
call wrapped by `chain.call`, which owns the progress line, the stream sink, the `-verbose` dump,
`ErrTruncated` and the usage tally. `-single` runs the draft alone and reproduces the request the
tool sent before the chain existed, byte for byte — `go run ./cmd/metaprompt -n -single` against
an older build is the check for that. Three rules hold the chain together:

- **The chain stays in `{$NAME}` space end to end.** Every step returns a template in the cookbook's
  syntax, so `steering.mustache` is reusable by all of them and `ToMustache` runs exactly once, on
  the last step's output. Converting early would mean converting back.
- **Drift is checked after every step and only fatal after the last.** `driftReport` verifies a
  converted *copy*; a non-empty report becomes the next step's `DRIFT` section, so a draft that
  drops a variable gets told what it dropped instead of ending the run.
- **Only the draft step carries the 25 KB metaprompt.** The chain costs roughly 1.6× a single call,
  not 4×. Keep it that way — `TestBuildReviewSteps` fails if it leaks into a review step.

`llm.Generate` streams (`goai.StreamText`), and `Request.Stream` is where the live copy goes —
`improve` points it at stdout, so the raw reply prints as it arrives while every other message stays
on stderr. `-stdout` is the exception: it claims stdout for the finished template and passes a nil
sink, because a live copy would corrupt `metaprompt -stdout foo.mustache > foo.1.mustache`. The
sink is a `Request` field rather than an argument so the `generate` test seam keeps its signature.

**Extended thinking is assumed on both sides, and they are different problems.** Our own calls are
expected to run with it — but **never set a thinking or effort parameter to get that.** There is no
such field on `llm.Request` and no flag for one; how much the model reasons is the model's own
default, the same hands-off stance as the absent model list. The prompts the tool *writes* are meant
to be run with thinking too, and that one fights the tool's core: `metaprompt.mustache` is
2024 text whose worked examples teach Claude to write prompts containing `<Scratchpad>` blocks and
"think step by step" instructions, and that file cannot be edited. So `thinking.mustache` undoes it
from the refine step onward — deliberately not at the draft step, where one paragraph would be
arguing with 25 KB of examples demonstrating the opposite. **The examples the draft writes matter
more than its instructions here**: strip the instruction and leave an example that still walks
through its reasoning and the example wins.

One consequence rides along: `TextResult.Text` from `goai.StreamText` folds reasoning chunks into
the text for backward compatibility, so whenever the model reasons, it is the thinking and the
answer run together. `llm.replyText` reads `Steps[n].Text` instead. Anything new that reads a reply
must go through that, or it will be extracting from the model's notes — and since we leave thinking
to the model, that is the normal case, not an edge one.

**`internal/metaprompt/metaprompt.mustache` is verbatim upstream text. Never edit, reformat, rewrap
or "fix" it** — not its duplicated `Note:` line, not its missing trailing newline. It is the
`metaprompt = """..."""` literal from cell 6 of `metaprompt.ipynb` (which contains no escape
sequences, so the bytes between the triple quotes are the string's exact value).
`TestMetapromptIsVerbatim` pins the length, head, tail and slot count; if it fails, the file was
modified, and the fix is to restore it, not to update the constants. Regenerate with:

```bash
python3 -c 'import json; src="".join(json.load(open("metaprompt.ipynb"))["cells"][6]["source"]); \
  open("internal/metaprompt/metaprompt.mustache","w").write(src[src.index(chr(34)*3)+3:src.rindex(chr(34)*3)])'
```

**Two placeholder syntaxes are in play and they are not interchangeable.** Prompt files are mustache
(`{{NAME}}`); the metaprompt only knows the cookbook's `{$NAME}` form and writes its templates that
way. `internal/metaprompt/tags.go` owns the round trip — read the tags out of the source, hand the
metaprompt a `{$NAME}` list to pin itself to, convert the reply back restoring each name's original
sigil, then `Verify` that nothing drifted. That verification is the product's promise, not a nicety:
an improved revision has to stay drop-in for whatever already renders the original, so a dropped
variable, a rename, a `{{{x}}}` silently demoted to `{{x}}` (escaping changes!), or a lost section
fails the run instead of writing a broken template. `-no-verify` is the escape hatch.

**Render mustache with `mustache.RenderRaw(tmpl, true, ctx)` — never plain `Render`.** The default
renderer HTML-escapes interpolated values, which would turn the XML tags this whole prompt is built
from into `&lt;…&gt;`. Every render site in `request.go` already does this; new ones must too.

**All GoAI/vendor imports stay behind `internal/llm`** (the rule from `nisaba/backend`). There is
deliberately no fixed model list here — the `-model` id passes straight through to
`anthropic.Chat`, so a model released tomorrow works without a code change. `ANTHROPIC_API_KEY` is
read from the environment by GoAI; `llm.Generate` checks it up front only so a missing key fails
immediately instead of as an auth error seconds later. `Request.Temperature` is a pointer because
unset and zero are different requests: Sonnet 5 and later reject `temperature=0`, which is why the
notebook is pinned to an older model.

## Working on this

- `make check` = fmt + vet + test. All tests run offline: `cmd/metaprompt` stubs the network calls
  through the package-level `generate` variable, so the whole flow after the API call is covered
  without a key. `stubReplies` scripts one reply per step in order and fails on an unscripted call —
  a test that scripts too few replies is a test that silently stopped exercising the later steps.
- `go run ./cmd/metaprompt -n testdata/example.mustache` prints all four requests and spends
  nothing. Reach for this first when changing anything about the prompt assembly. Only step 1 is
  exactly what would be sent; the rest show upstream outputs as `«output of step N …»` placeholders.
- **End-to-end runs always use `-m claude-haiku-4-5`** — cheapest and fastest, and nothing here is
  model-specific. Never burn the `claude-sonnet-5` default on a verification run. Delete the
  `testdata/example.N.mustache` files a run leaves behind rather than committing them. After such a
  run, `grep -Ei 'scratchpad|step.by.step|<thinking>'` the result: a hit means `thinking.mustache`
  isn't landing. Then read the examples in it by eye — a clean grep with an example that still
  reasons before answering is the exact half-done state that step exists to prevent.
- The seven request templates (`metaprompt`, `task`, `steering`, `analyze`, `refine`, `polish`,
  `thinking` under `internal/metaprompt/`) are each independently overridable at runtime via
  `-prompts-dir`, following the per-file fallback pattern in
  `nisaba/backend/internal/mode/templates.go`. Prefer trying a wording change through that flag
  before editing the embedded copy.
- `steering.mustache` is ported from notebook cell 12 and exists because Claude 4.6+ rejects
  assistant-message prefill; the notebook used prefill to force the reply to start at `<Inputs>`,
  and this steers from the user turn instead.
- Go's RE2 has no backreferences, so cell 16's `<(\w+)></\1>$` becomes two captures compared in
  `removeEmptyTags`. Keep that in mind when porting anything else from the notebook.
