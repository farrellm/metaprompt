# metaprompt

A CLI that improves LLM prompts. Point it at a prompt stored as a mustache template and it writes
an improved next revision beside it, using Anthropic's metaprompt to do the rewriting.

```console
$ metaprompt summarize.mustache
step 1/4 analyze: improving summarize.mustache with claude-sonnet-5 (2 variables)...
<brief>
...                      # the reply, printed as the model writes it
step 2/4 draft: improving summarize.mustache with claude-sonnet-5 (2 variables)...
<Inputs>
{$TRANSCRIPT}
{$AUDIENCE}
</Inputs>
...
step 3/4 refine: ...
step 4/4 polish: ...
wrote summarize.1.mustache

$ metaprompt -g "make it more concise" summarize.1.mustache
wrote summarize.2.mustache
```

The original is never modified — each improvement lands as `name.1.mustache`, `name.2.mustache`, and
so on, so a revision you don't like is deleted rather than undone, and a prompt's whole history is
one directory listing. Improving a revision continues the same series.

**Rewrites stay drop-in compatible.** The new revision must interpolate exactly the same mustache
variables, the same way, and keep every section, partial and comment the original had. If the
rewrite drifts, it is reported and not written — so whatever renders your prompt keeps working
across revisions.

## Install

```console
go install github.com/farrellm/metaprompt/cmd/metaprompt@latest
```

Set `ANTHROPIC_API_KEY` in your environment. Go 1.25 or later.

## Usage

```
metaprompt [flags] <file.mustache>

  -m, -model string        Anthropic model id (default "claude-sonnet-5")
  -g, -guidance string     extra instruction for the rewrite, e.g. "make it more concise"
  -o, -out string          write to this path instead of the next revision
  -n, -dry-run             print the requests that would be sent and exit
  -v, -verbose             print the full reply and token usage to stderr
      -single              make one metaprompt call instead of the four-step chain
      -stdout              write the result to stdout instead of a file (silences the live reply)
      -max-tokens int      output token limit (default 8192)
      -temperature float   sampling temperature; unset by default (Sonnet 5+ rejects 0)
      -prompts-dir string  directory of .mustache overrides
      -no-verify           downgrade variable-drift errors to warnings and write anyway
```

The reply streams to stdout as the model writes it, so a slow rewrite is something to watch rather
than a silent wait. Everything else — the progress line, warnings, `wrote <path>` — goes to stderr,
so `metaprompt foo.mustache > reply.txt` keeps just the reply. The exception is `-stdout`, which
claims stdout for the finished template and turns the live copy off.

`-dry-run` costs nothing and prints all four requests — the fastest way to see what the tool is
actually asking for. Only the first is exactly what would be sent; the later steps quote outputs
that don't exist yet, so those appear as labelled placeholders.

`-prompts-dir` overrides the built-in prompts one file at a time (`metaprompt.mustache`,
`task.mustache`, `steering.mustache`, `analyze.mustache`, `refine.mustache`, `polish.mustache`,
`thinking.mustache`); anything absent from the directory keeps its default, so tuning the steering
doesn't oblige you to copy the 25 KB metaprompt.

## How it works

The metaprompt is a long multi-shot prompt, full of worked examples, that teaches a model to write
good prompt templates from a task description. This tool feeds it your existing prompt as the task
("here is a prompt template, rewrite it"), pins the rewrite to your variables, then pulls the
`<Instructions>` block out of the reply and converts the metaprompt's `{$NAME}` placeholders back to
mustache `{{NAME}}`.

That is one model call doing five jobs at once, so it runs as a chain of four instead:

| step | job |
|---|---|
| **analyze** | works out what the original prompt actually requires — a numbered requirement list, what each variable is for, what each example demonstrates, where it is weak |
| **draft** | the metaprompt call, now carrying that brief |
| **refine** | checks the draft against every requirement and repairs anything the rewrite dropped |
| **polish** | a last editing pass on how it reads, not on what it asks for |

Drift is checked after every step but only fatal after the last: a draft that drops a variable is
handed its own drift report to repair. `-single` collapses the chain back to the one metaprompt
call, for a cheap pass.

**Prompts are written for a model with extended thinking**, on the assumption you will run them that
way — as this tool's own calls are. It sets no thinking or effort parameter to get that: how much a
model reasons before answering is left to the model. That assumption puts the tool at odds with the
metaprompt itself, though: the cookbook text predates extended thinking, and its examples
teach the model to write prompts containing scratchpad blocks and "think step by step" instructions.
That file is reproduced verbatim and can't be changed, so the refine and polish steps undo it
afterwards — in the examples the draft writes as well as its instructions, since a prompt whose
examples demonstrate the behaviour its instructions disclaim will get the behaviour.

## The notebook

[`metaprompt.ipynb`](metaprompt.ipynb) is the interactive version and the provenance of the prompt
text: enter a task, get a prompt template, test it on example values. Use it to write a prompt from
scratch, and the CLI to iterate on one you already have.

## Attribution

`metaprompt.ipynb` is adapted from [`misc/metaprompt.ipynb`](https://github.com/anthropics/claude-cookbooks/blob/main/misc/metaprompt.ipynb)
in Anthropic's [claude-cookbooks](https://github.com/anthropics/claude-cookbooks) repository, used
under the MIT License. The metaprompt text itself is unchanged; the surrounding code has been
updated for current Claude models and the current `anthropic` SDK.

The same text is reproduced byte-for-byte at
[`internal/metaprompt/metaprompt.mustache`](internal/metaprompt/metaprompt.mustache), which is what
the CLI sends.

## License

MIT — see [LICENSE](LICENSE), which covers both the original Anthropic copyright and modifications
in this repository.
