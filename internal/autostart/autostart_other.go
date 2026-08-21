//go:build !windows

package autostart

func Available() bool        { return false }
func Enabled() (bool, error) { return false, nil }
func SetEnabled(bool) error  { return nil }
func Reconcile() error       { return nil }
