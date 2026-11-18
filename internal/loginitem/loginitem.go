// Package loginitem 封装 macOS 登录项（SMAppService）的状态查询与注册。
// 系统状态是唯一真值，应用配置不单独保存该开关。
package loginitem

import "errors"

// Status 表示登录项的系统实际状态。
type Status string

const (
	StatusNotRegistered    Status = "not_registered"
	StatusEnabled          Status = "enabled"
	StatusRequiresApproval Status = "requires_approval"
	StatusNotFound         Status = "not_found"
	StatusUnsupported      Status = "unsupported"
)

var (
	// ErrUnsupported 表示当前系统或平台不支持登录项。
	ErrUnsupported = errors.New("当前系统不支持登录项")
	// ErrOperationFailed 表示系统拒绝了登录项操作。
	ErrOperationFailed = errors.New("登录项操作未成功")
)

// StatusFromCode 把 SMAppServiceStatus 映射为稳定状态。
func StatusFromCode(code int) Status {
	switch code {
	case 0:
		return StatusNotRegistered
	case 1:
		return StatusEnabled
	case 2:
		return StatusRequiresApproval
	case 3:
		return StatusNotFound
	default:
		return StatusUnsupported
	}
}

// Supported 表示当前平台是否提供登录项能力。
func (s Status) Supported() bool {
	return s != StatusUnsupported
}

// SetEnabled 注册或注销登录项；已处于目标状态时幂等返回。
func SetEnabled(enabled bool) error {
	current := CurrentStatus()
	if !current.Supported() {
		return ErrUnsupported
	}
	if enabled {
		if current == StatusEnabled || current == StatusRequiresApproval {
			return nil
		}
		return register()
	}
	if current == StatusNotRegistered || current == StatusNotFound {
		return nil
	}
	return unregister()
}
