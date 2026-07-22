// Package httplimits contains transport-level limits shared by every Scribe
// HTTP process.
package httplimits

// MaxHeaderBytes bounds the aggregate request-header allocation. Authentication
// tokens and forwarding metadata fit comfortably inside 64 KiB.
const MaxHeaderBytes = 64 << 10
