package bot

import _ "embed"

// embeddedBasePrompt ships the shared human-behavior rules inside the
// binary so an operator with no data/rules/base-human.md still gets the
// full behavior contract — open-source users clone, build, run, done.
// Dropping a file at cfg.BasePromptFile overrides it at runtime.
//
//go:embed rules/base-human.md
var embeddedBasePrompt string
