// Package version pins the single source of truth for harmock's version.
package version

// Version is the semantic version of this build. It must match go.mod's
// header comment and CHANGELOG.md; scripts/smoke.sh asserts on it.
const Version = "0.1.0"
