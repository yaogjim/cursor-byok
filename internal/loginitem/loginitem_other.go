//go:build !darwin

package loginitem

// CurrentStatus 在非 macOS 平台返回不支持。
func CurrentStatus() Status {
	return StatusUnsupported
}

func register() error {
	return ErrUnsupported
}

func unregister() error {
	return ErrUnsupported
}
