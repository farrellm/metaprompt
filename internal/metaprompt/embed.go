// Package metaprompt turns an existing mustache prompt template into a request
// for Anthropic's metaprompt, and turns the model's reply back into a mustache
// template that is drop-in compatible with the original.
//
// The metaprompt itself is upstream text, reproduced verbatim — see Metaprompt.
package metaprompt

import _ "embed"

// Metaprompt is the metaprompt from Anthropic's claude-cookbooks, reproduced
// byte-for-byte from the `metaprompt = """..."""` literal in cell 6 of
// metaprompt.ipynb. It is a multi-shot prompt whose examples teach the model to
// write good prompt templates, with a single {{TASK}} slot for the task at hand.
//
// Treat metaprompt.mustache as immutable upstream text: do not edit, re-wrap or
// "fix" it, including its duplicated "Note:" line and its missing trailing
// newline. TestMetapromptIsVerbatim guards those bytes.
//
//go:embed metaprompt.mustache
var Metaprompt string
