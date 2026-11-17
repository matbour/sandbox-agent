// Package fcapi holds the types of Firecracker's HTTP API, generated from the OpenAPI
// specification Firecracker publishes for each release (src/firecracker/swagger/firecracker.yaml),
// pinned in firecracker.yaml to the version in firecracker.version. The official Go SDK is
// archived and stops at the 1.4 API (D54).
//
// Optional fields are pointers, so an explicit false or zero is sent; Ptr makes one.
//
// To follow a new Firecracker release: make firecracker-api VERSION=v1.x.y, then go generate.
package fcapi

//go:generate go run ./gen firecracker.yaml types.go

// Ptr returns a pointer to v, for the optional fields.
func Ptr[T any](v T) *T { return &v }
