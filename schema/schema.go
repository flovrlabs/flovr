// Package schema holds the JSON Schemas of Flovr's files, workflow.yaml and
// component.yaml, for editors, coding agents and CI. flovr schema prints them.
package schema

import _ "embed"

var (
	//go:embed workflow.json
	Workflow []byte
	//go:embed component.json
	Component []byte
)
