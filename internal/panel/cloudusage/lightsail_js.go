//go:build js

package cloudusage

// Cloud usage is disabled on Edge. Keep the native AWS SDK out of the Worker bundle.
func newLightsail() provider { return nil }
