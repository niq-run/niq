// Package project owns the project definition and one project's runtime
// assembly.
//
// A project lives under <niq root>/projects/<id>/ and is the unit niq runs: one
// directory holding its metadata, worker declarations and runtime state. This
// package creates, lists, links, loads and exports that definition, and in
// run.go assembles exactly one project in its own process — its bus, event
// store, worker control plane and WebUI. It knows no other project.
//
// Parsing a template file is not here: app/standalone/template does that and
// hands over a validated *Template, which this package instantiates per
// manifest. The worker model itself — manifests, params, the per-type spawn
// contract, placement, supervision — lives in app/standalone/worker. The type
// aliases below keep the project's own vocabulary (a "worker config") so
// existing code and tests keep their spelling.
package project

import (
	projtemplate "github.com/niq-run/niq/app/standalone/template"
	projworker "github.com/niq-run/niq/app/standalone/worker"
)

// Worker manifests. Aliased (not redeclared) so project code and the control
// plane share one definition.
type (
	// WorkerConfig is one worker declaration, as stored in workers.json.
	WorkerConfig = projworker.Manifest
	// TemplateConfig is the top-level structure of a template file. It lives in
	// the template package (a template is a project seed, not a worker); the
	// alias keeps the project's own spelling.
	TemplateConfig = projtemplate.Template
)
