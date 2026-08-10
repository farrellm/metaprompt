# metaprompt

A CLI that improves an LLM prompt stored as a mustache template: read `name.mustache`, ask Claude —
through Anthropic's metaprompt — to rewrite it, write the result as `name.1.mustache`. The pipeline
is one straight line in `improve()` (`cmd/metaprompt/main.go`): read the file → `ParseTags` →
`Templates.BuildRequest` → one `llm.Generate` call → `ExtractInstructions` → `Tags.ToMustache` →
`Tags.Verify` → `revision.NextPath` → write. There is no tool loop and no config file.

`llm.Generate` streams (`goai.StreamText`), and `Request.Stream` is where the live copy goes —
`improve` points it at stdout, so the raw reply prints as it arrives while every other message stays
on stderr. `-stdout` is the exception: it claims stdout for the finished template and passes a nil
sink, because a live copy would corrupt `metaprompt -stdout foo.mustache > foo.1.mustache`. The
sink is a `Request` field rather than an argument so the `generate` test seam keeps its signature.

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

- `make check` = fmt + vet + test. All tests run offline: `cmd/metaprompt` stubs the one network
  call through the package-level `generate` variable, so the whole flow after the API call is
  covered without a key.
- `go run ./cmd/metaprompt -n testdata/example.mustache` prints the exact request and spends
  nothing. Reach for this first when changing anything about the prompt assembly.
- **End-to-end runs always use `-m claude-haiku-4-5`** — cheapest and fastest, and nothing here is
  model-specific. Never burn the `claude-sonnet-5` default on a verification run. Delete the
  `testdata/example.N.mustache` files a run leaves behind rather than committing them.
- The three request templates (`metaprompt`, `task`, `steering` under `internal/metaprompt/`) are
  each independently overridable at runtime via `-prompts-dir`, following the per-file fallback
  pattern in `nisaba/backend/internal/mode/templates.go`. Prefer trying a wording change through
  that flag before editing `task.mustache`/`steering.mustache`.
- `steering.mustache` is ported from notebook cell 12 and exists because Claude 4.6+ rejects
  assistant-message prefill; the notebook used prefill to force the reply to start at `<Inputs>`,
  and this steers from the user turn instead.
- Go's RE2 has no backreferences, so cell 16's `<(\w+)></\1>$` becomes two captures compared in
  `removeEmptyTags`. Keep that in mind when porting anything else from the notebook.
