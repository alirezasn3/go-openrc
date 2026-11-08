package goOpenRC

// OpenRC only exists on linux. On every other platform these functions report
// ErrUnsupportedPlatform rather than silently pretending to succeed.

// Available always returns false off linux.
func Available() bool { return false }

func CreateService(service *Service) error { return ErrUnsupportedPlatform }

func DeleteService(name string) error { return ErrUnsupportedPlatform }

func StartService(name string) error { return ErrUnsupportedPlatform }

func StopService(name string) error { return ErrUnsupportedPlatform }

func RestartService(name string) error { return ErrUnsupportedPlatform }

func StatusService(name string) (*Status, error) { return nil, ErrUnsupportedPlatform }

func DaemonReload() error { return ErrUnsupportedPlatform }
