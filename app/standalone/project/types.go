// Package project provides the storage and runtime for niq projects.
//
// A project lives under ~/.niq/projects/<id>/ and owns its own event bus,
// WebUI, worker set and state directories. This package reads project
// templates (JSON), instantiates workers, registers them with WorkerService,
// and manages the project instance lifecycle. This is the only place where
// template files are parsed into worker instances — no new abstractions.
//
// The worker model itself — declarations, their params projection, the
// per-type spawn contract, placement, process supervision, provisioning and
// the lifecycle backend — lives in app/project/worker. The type aliases below
// keep the project's own vocabulary (a "worker config") and let existing code
// and tests keep their spelling while the implementation lives in the subpackage.
package project

import (
	projtemplate "github.com/niq-run/niq/app/standalone/project/template"
	projworker "github.com/niq-run/niq/app/standalone/worker"
)

// Worker declarations and their ACL / program shapes. Aliased (not redeclared)
// so project code and the lifecycle share one definition.
type (
	// WorkerConfig is one worker declaration, as stored in workers.json.
	WorkerConfig = projworker.Declaration
	// TemplateConfig is the top-level structure of a template file. It lives in
	// the template package (a template is a project seed, not a worker); the
	// alias keeps the project's own spelling.
	TemplateConfig = projtemplate.Template
	// SubscriptionSpec is one SubscribeAllow entry.
	SubscriptionSpec = projworker.SubscriptionSpec
	// PublishSpec is one PublishAllow entry.
	PublishSpec = projworker.PublishSpec
)
