//go:build darwin

package loginitem

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework ServiceManagement -framework Foundation
#include <ServiceManagement/ServiceManagement.h>

static int loginItemStatusCode(void) {
	if (@available(macOS 13.0, *)) {
		return (int)[[SMAppService mainAppService] status];
	}
	return -1;
}

static int loginItemRegister(void) {
	if (@available(macOS 13.0, *)) {
		NSError *error = nil;
		BOOL ok = [[SMAppService mainAppService] registerAndReturnError:&error];
		return ok ? 0 : 1;
	}
	return 2;
}

static int loginItemUnregister(void) {
	if (@available(macOS 13.0, *)) {
		NSError *error = nil;
		BOOL ok = [[SMAppService mainAppService] unregisterAndReturnError:&error];
		return ok ? 0 : 1;
	}
	return 2;
}
*/
import "C"

// CurrentStatus 返回登录项的当前系统状态。
func CurrentStatus() Status {
	return StatusFromCode(int(C.loginItemStatusCode()))
}

func register() error {
	if int(C.loginItemRegister()) != 0 {
		return ErrOperationFailed
	}
	return nil
}

func unregister() error {
	if int(C.loginItemUnregister()) != 0 {
		return ErrOperationFailed
	}
	return nil
}
