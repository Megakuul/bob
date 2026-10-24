// Package builder is the program that runs inside bob derivations (SPEC §1.12, R-NIX-015..019):
// it reads the job from the structured attributes, runs the kind's work confined to the outputs
// and the build directory, runs the fixup contract and reports through exit codes.
package builder
