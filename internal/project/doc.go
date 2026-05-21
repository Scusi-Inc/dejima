// Package project manages per-project configuration and lifecycle state.
//
// Each island is represented by a project: (name, repo, agent, resources,
// desired state). Config persists to ~/.dejima/projects/<name>/config.toml.
package project
