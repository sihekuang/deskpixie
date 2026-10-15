package agent

import _ "embed"

// DelegationPersona is the delegation rules (assets/delegation.md, verbatim from ai-mascot),
// appended to Pixie's persona as "{persona}\n\n{DelegationPersona}" (Rust tools::DELEGATION_PERSONA).
//
//go:embed assets/delegation.md
var DelegationPersona string
