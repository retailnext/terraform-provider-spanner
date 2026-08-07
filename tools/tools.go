// Copyright RetailNext, Inc. 2026

// Package tools pins the doc/lint tooling used by `go generate` below, via
// go.mod's `tool` directive (Go 1.24+) rather than the older blank-import
// pattern
package tools

// Generate copyright headers
//go:generate go tool copywrite headers -d .. --config ../.copywrite.hcl

// Format Terraform code for use in documentation.
// If you do not have Terraform installed, you can remove the formatting command, but it is suggested
// to ensure the documentation is formatted properly.
//go:generate terraform fmt -recursive ../examples/

// Generate documentation.
//go:generate go tool tfplugindocs generate --provider-dir .. -provider-name spanner
