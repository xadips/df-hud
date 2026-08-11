//go:build !linux || !cgo || nolayershell

// No-op counterpart to layershell.go, selected by `-tags nolayershell` or when
// cgo is off. Exists so `go vet` and the non-UI tests run on a machine with no
// Wayland, no GTK4 and no gtk4-layer-shell installed - CI, a container, or a
// quick check over ssh. Supported() reporting false is what makes main.go
// refuse to render rather than putting up a plain window that looks broken.
package main

type Layer int

const (
	LayerBackground Layer = iota
	LayerBottom
	LayerTop
	LayerOverlay
)

type Edge int

const (
	EdgeLeft Edge = iota
	EdgeRight
	EdgeTop
	EdgeBottom
)

type KeyboardMode int

const (
	KeyboardNone KeyboardMode = iota
	KeyboardExclusive
	KeyboardOnDemand
)

const LayerShellBuilt = false

func Supported() bool                       { return false }
func Version() (int, int, int)              { return 0, 0, 0 }
func ProtocolVersion() int                  { return 0 }
func InitForWindow(uintptr)                 {}
func IsLayerWindow(uintptr) bool            { return false }
func SetNamespace(uintptr, string)          {}
func SetLayer(uintptr, Layer)               {}
func SetAnchor(uintptr, Edge, bool)         {}
func SetMargin(uintptr, Edge, int)          {}
func SetExclusiveZone(uintptr, int)         {}
func SetKeyboardMode(uintptr, KeyboardMode) {}
func SetMonitor(uintptr, uintptr)           {}
func SetClickThrough(uintptr, bool) bool    { return false }
