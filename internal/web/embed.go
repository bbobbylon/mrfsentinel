// Package web is MRF Sentinel's HTTP layer: routing, request handlers, and
// the server-rendered dashboard a compliance officer actually uses.
//
// There's no separate frontend project here, no npm, no JavaScript build
// step. Every page is a Go html/template rendered on the server, and the
// finished HTML/CSS/JS are compiled directly into this binary via go:embed
// below — the same reason DeleteBoard needed a `bundled` Maven profile and
// a frontend-maven-plugin to fold its separately-built Angular app into one
// jar, this project gets for free from Go's standard library, because
// there's no separate app to build in the first place. See
// ARCHITECTURE.md for the fuller case for why a compliance-report tool
// doesn't need a SPA.
package web

import "embed"

// templateFS holds every page template, compiled into the binary at build
// time rather than read from disk at runtime. That is what lets the
// deployed artifact be a single file, with no templates/ directory shipped
// alongside it. loadTemplates (templates.go) parses these once at startup.
//
//go:embed templates/*.html
var templateFS embed.FS

// staticFS holds the stylesheet and the one small JavaScript file, served
// directly out of the binary by NewRouter (router.go). Same reasoning as
// templateFS above: nothing to copy at deploy time, nothing to mount, and
// no CDN to configure for an app this size.
//
//go:embed static/*
var staticFS embed.FS
