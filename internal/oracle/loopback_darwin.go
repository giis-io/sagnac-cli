package oracle

// liftLoopback has nothing to do on macOS: the network cut keeps lo.
func liftLoopback() error { return nil }
