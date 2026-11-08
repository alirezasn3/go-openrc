package goOpenRC

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"

	goPermissions "github.com/alirezasn3/go-permissions"
)

// Available reports whether this machine has the OpenRC tooling installed.
func Available() bool {
	for _, binary := range []string{"rc-service", "rc-update"} {
		if _, e := exec.LookPath(binary); e != nil {
			return false
		}
	}
	return true
}

func checkOpenRC() error {
	if !Available() {
		return ErrOpenRCNotFound
	}
	return nil
}

// run runs an OpenRC command and normalizes its error.
func run(command string, args ...string) ([]byte, error) {
	b, e := exec.Command(command, args...).CombinedOutput()
	if e != nil {
		out := string(b)
		for _, denied := range []string{"must be root", "Permission denied", "permission denied", "superuser"} {
			if strings.Contains(out, denied) {
				return b, ErrPermissionDenied
			}
		}
		return b, fmt.Errorf("%s %s: %s: %s", command, strings.Join(args, " "), e.Error(), strings.TrimSpace(out))
	}
	return b, nil
}

// DaemonReload forces OpenRC to rebuild its dependency cache. OpenRC has no
// real equivalent of systemd's daemon-reload; this runs `rc-update -u`.
func DaemonReload() error {
	if e := checkOpenRC(); e != nil {
		return e
	}
	_, e := run("rc-update", "-u")
	return e
}

// exists reports whether OpenRC can resolve the service.
// `rc-service -e` exits 0 when the init script is found.
func exists(name string) bool {
	return exec.Command("rc-service", "-e", name).Run() == nil
}

// runlevels lists the runlevels the service is enabled in, by parsing the
// "<name> | <runlevel> <runlevel>" lines of `rc-update show`.
func runlevels(name string) []string {
	b, e := exec.Command("rc-update", "show").CombinedOutput()
	if e != nil {
		return nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		service, levels, found := strings.Cut(line, "|")
		if found && strings.TrimSpace(service) == name {
			return strings.Fields(levels)
		}
	}
	return nil
}

// readPid reads the pidfile the scripts we generate point at. Best effort: it
// stays 0 for services installed by someone else.
func readPid(name string) int {
	b, e := os.ReadFile("/run/" + name + ".pid")
	if e != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

// StatusService returns the current state of the given service.
// A missing service is not an error: the returned Status has Exists set to false.
func StatusService(name string) (*Status, error) {
	if e := checkOpenRC(); e != nil {
		return nil, e
	}

	name = serviceName(name)
	if name == "" {
		return nil, errors.New("service name is empty")
	}

	status := &Status{Name: name}
	if !exists(name) {
		return status, nil
	}
	status.Exists = true

	raw, _ := exec.Command("rc-service", name, "status").CombinedOutput()
	status.Raw = string(raw)
	status.State = parseState(status.Raw)
	status.Active = status.State == "started"

	status.Runlevels = runlevels(name)
	status.Enabled = len(status.Runlevels) > 0
	status.MainPID = readPid(name)

	return status, nil
}

// StartService starts the given service.
func StartService(name string) error {
	status, e := StatusService(name)
	if e != nil {
		return e
	}
	if !status.Exists {
		return ErrServiceNotFound
	}
	_, e = run("rc-service", status.Name, "start")
	return e
}

// StopService stops the given service.
func StopService(name string) error {
	status, e := StatusService(name)
	if e != nil {
		return e
	}
	if !status.Exists {
		return ErrServiceNotFound
	}
	_, e = run("rc-service", status.Name, "stop")
	return e
}

// RestartService restarts the given service.
func RestartService(name string) error {
	status, e := StatusService(name)
	if e != nil {
		return e
	}
	if !status.Exists {
		return ErrServiceNotFound
	}
	_, e = run("rc-service", status.Name, "restart")
	return e
}

// CreateService writes the init script to /etc/init.d/, adds the service to its
// runlevel and starts it. An init script that already exists is left alone.
func CreateService(service *Service) error {
	if service == nil {
		return errors.New("service is nil")
	}
	if e := checkOpenRC(); e != nil {
		return e
	}
	if e := service.validate(); e != nil {
		return e
	}

	status, e := StatusService(service.Name)
	if e != nil {
		return e
	}

	if !status.Exists {
		// check if user has permission to write to /etc/init.d/
		permissions, e := goPermissions.GetPermissions(InitDir)
		if e != nil {
			return e
		}
		if !permissions.Write {
			return errors.New("you don't have write access to " + InitDir)
		}

		// write init script, which has to be executable
		path := InitDir + status.Name
		if e = os.WriteFile(path, []byte(service.InitScript()), 0755); e != nil {
			return e
		}
		// WriteFile applies the umask and leaves an existing file's mode alone
		if e = os.Chmod(path, 0755); e != nil {
			return e
		}

		if e = DaemonReload(); e != nil {
			return e
		}
	}

	// add service to its runlevel
	if _, e = run("rc-update", "add", status.Name, service.runlevel()); e != nil {
		return e
	}

	// start service
	if _, e = run("rc-service", status.Name, "start"); e != nil {
		return e
	}

	return nil
}

// DeleteService stops the service, removes it from every runlevel and deletes
// its init script from /etc/init.d/.
func DeleteService(name string) error {
	if e := checkOpenRC(); e != nil {
		return e
	}

	status, e := StatusService(name)
	if e != nil {
		return e
	}
	if !status.Exists {
		return ErrServiceNotFound
	}

	// check if user has permission to delete file from /etc/init.d/
	permissions, e := goPermissions.GetPermissions(InitDir)
	if e != nil {
		return e
	}
	if !permissions.Write {
		return errors.New("you don't have write access to " + InitDir)
	}

	// stop service
	if _, e = run("rc-service", status.Name, "stop"); e != nil {
		return e
	}

	// remove service from every runlevel it was added to
	if status.Enabled {
		if _, e = run("rc-update", "-a", "delete", status.Name); e != nil {
			return e
		}
	}

	// remove init script
	if e = os.Remove(InitDir + status.Name); e != nil {
		if !errors.Is(e, fs.ErrNotExist) {
			return e
		}
		return errors.New(status.Name + " is not defined in " + InitDir + " and cannot be removed")
	}

	return DaemonReload()
}
