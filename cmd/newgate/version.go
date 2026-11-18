package main

// Build facts injected at link time, written by the Makefile's ldflags (see LDFLAGS in the
// Makefile).
//
// They are declared only here and read exactly once by entry.MakeProcess: that pass normalizes
// argv0 and drops the version into the buildinfo leaf, and after that every module (UI, daemon)
// reads the same copy (see lib/buildinfo).
var (
	version    = "dev"
	buildTime  = "unknown"
	commitTime = "unknown"
)
