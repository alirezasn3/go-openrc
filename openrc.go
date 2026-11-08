// Package goOpenRC manages OpenRC services. It generates /etc/init.d/ scripts
// and drives rc-service and rc-update. On non linux builds every function that
// touches the system returns ErrUnsupportedPlatform.
//
// The exported API deliberately mirrors github.com/alirezasn3/go-systemd, so a
// program can switch between the two by changing its import.
package goOpenRC

import (
	"errors"
	"strconv"
	"strings"
	"time"
)

// InitDir is where OpenRC init scripts live.
const InitDir = "/etc/init.d/"

// ErrServiceNotFound is returned when OpenRC cannot resolve the service.
var ErrServiceNotFound = errors.New("service not found")

// ErrPermissionDenied is returned when the operation needs root.
var ErrPermissionDenied = errors.New("permission denied")

// ErrUnsupportedPlatform is returned by every function on non linux builds.
var ErrUnsupportedPlatform = errors.New("only linux is supported")

// ErrOpenRCNotFound is returned when rc-service or rc-update is missing, which
// means this machine is not running OpenRC.
var ErrOpenRCNotFound = errors.New("openrc not found (rc-service and rc-update are required)")

// Service describes the service to install. Name and ExecStart are required.
//
// The first group of fields is named after systemd directives and is translated
// to the closest OpenRC equivalent; see the README for the mapping. The second
// group is OpenRC native and has no systemd counterpart here.
type Service struct {
	Name        string
	ExecStart   string
	Restart     string
	RestartSec  string
	Description string
	After       string // -> after in depend()
	Wants       string // -> want in depend()
	WantedBy    string // -> runlevel, unless Runlevel is set

	Need     string // -> need in depend(), a hard dependency
	Use      string // -> use in depend(), used only if in the same runlevel
	Runlevel string // runlevel to add the service to, defaults to "default"
}

// Status holds the state of an OpenRC service.
type Status struct {
	Name      string   // service name as OpenRC knows it
	Exists    bool     // false when OpenRC cannot resolve the service
	Active    bool     // State == "started"
	Enabled   bool     // the service is in at least one runlevel
	State     string   // started, stopped, crashed, starting, stopping, inactive
	Runlevels []string // runlevels the service is enabled in
	MainPID   int      // read from the pidfile, 0 when unknown
	Raw       string   // output of `rc-service <name> status`
}

// validate checks the fields that end up in the generated init script.
func (service *Service) validate() error {
	if strings.TrimSpace(service.Name) == "" {
		return errors.New("service name is required")
	}
	if strings.TrimSpace(service.ExecStart) == "" {
		return errors.New("service ExecStart is required")
	}
	if strings.ContainsAny(service.Name, "/\\") {
		return errors.New("service name must not contain path separators")
	}
	// a newline in any field would let the caller inject arbitrary lines into
	// the generated init script
	fields := map[string]string{
		"Name": service.Name, "ExecStart": service.ExecStart, "Restart": service.Restart,
		"RestartSec": service.RestartSec, "Description": service.Description,
		"After": service.After, "Wants": service.Wants, "WantedBy": service.WantedBy,
		"Need": service.Need, "Use": service.Use, "Runlevel": service.Runlevel,
	}
	for field, value := range fields {
		if strings.ContainsAny(value, "\n\r") {
			return errors.New("service " + field + " must not contain newlines")
		}
	}
	return nil
}

// serviceName normalizes a name: OpenRC service names are bare, so a systemd
// style ".service" suffix is dropped.
func serviceName(name string) string {
	return strings.TrimSuffix(strings.TrimSpace(name), ".service")
}

// shQuote wraps a value in single quotes so the generated init script treats it
// as a literal, whatever it contains.
func shQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// parseState pulls the state word out of `rc-service <name> status` output,
// which looks like " * status: started". The exit codes of that command are not
// documented, so the output is what we go by.
func parseState(out string) string {
	if _, after, found := strings.Cut(out, "status:"); found {
		if fields := strings.Fields(after); len(fields) > 0 {
			return fields[0]
		}
	}
	for _, state := range []string{"started", "stopped", "crashed", "starting", "stopping", "inactive"} {
		if strings.Contains(out, state) {
			return state
		}
	}
	return ""
}

// dependency translates one systemd unit name into the closest OpenRC name.
func dependency(unit string) string {
	unit = strings.TrimSuffix(unit, ".target")
	unit = strings.TrimSuffix(unit, ".service")
	switch unit {
	case "network", "network-online":
		return "net"
	case "syslog", "systemd-journald":
		return "logger"
	}
	return unit
}

// dependencies translates a whole space or comma separated field.
func dependencies(field string) []string {
	var deps []string
	for _, unit := range strings.Fields(strings.ReplaceAll(field, ",", " ")) {
		if dep := dependency(unit); dep != "" {
			deps = append(deps, dep)
		}
	}
	return deps
}

// runlevel picks the runlevel to add the service to. Runlevel wins over
// WantedBy, which is translated from its systemd target name.
func (service *Service) runlevel() string {
	if level := strings.TrimSpace(service.Runlevel); level != "" {
		return level
	}
	fields := strings.Fields(strings.ReplaceAll(service.WantedBy, ",", " "))
	if len(fields) == 0 {
		return "default"
	}
	level := strings.TrimSuffix(fields[0], ".target")
	switch level {
	case "multi-user", "graphical", "default":
		return "default"
	}
	return level
}

// supervises reports whether Restart= asks for the process to be kept alive,
// which on OpenRC means handing it to supervise-daemon.
func supervises(restart string) bool {
	switch strings.TrimSpace(restart) {
	case "", "no":
		return false
	}
	return true
}

// seconds converts a systemd time span to whole seconds, which is what
// respawn_delay takes.
func seconds(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	// a bare number is seconds in systemd
	if n, e := strconv.Atoi(value); e == nil {
		return n
	}
	// systemd spells minutes "min", Go spells them "m"
	duration, e := time.ParseDuration(strings.ReplaceAll(value, "min", "m"))
	if e != nil {
		return 0
	}
	if whole := int(duration.Seconds()); whole > 0 {
		return whole
	}
	if duration > 0 {
		return 1 // respawn_delay cannot express sub-second delays
	}
	return 0
}

// InitScript renders the OpenRC init script for the service, without writing
// anything. Useful for inspecting what CreateService would install.
func (service *Service) InitScript() string {
	name := serviceName(service.Name)

	description := service.Description
	if description == "" {
		description = name
	}

	// systemd splits ExecStart on whitespace too: first field is the binary
	fields := strings.Fields(service.ExecStart)
	if len(fields) == 0 {
		return "" // validate() rejects this; guard so we never index into nothing
	}
	command := fields[0]
	args := strings.Join(fields[1:], " ")

	text := "#!/sbin/openrc-run\n\n"
	text += "description=" + shQuote(description) + "\n"
	text += "command=" + shQuote(command) + "\n"
	if args != "" {
		text += "command_args=" + shQuote(args) + "\n"
	}
	text += "pidfile=" + shQuote("/run/"+name+".pid") + "\n"

	if supervises(service.Restart) {
		// supervise-daemon respawns the process when it dies, which is the
		// closest thing to Restart=. It wants the daemon in the foreground, so
		// command_background must stay unset.
		text += "supervisor=supervise-daemon\n"
		text += "respawn_max=0\n" // 0 means unlimited
		if delay := seconds(service.RestartSec); delay > 0 {
			text += "respawn_delay=" + strconv.Itoa(delay) + "\n"
		}
	} else {
		text += "command_background=true\n"
	}

	// systemd would send this to the journal; OpenRC drops it unless redirected
	text += "output_log=" + shQuote("/var/log/"+name+".log") + "\n"
	text += "error_log=" + shQuote("/var/log/"+name+".log") + "\n"

	// need is hardest, after is ordering only
	depend := ""
	for _, group := range []struct {
		keyword string
		field   string
	}{
		{"need", service.Need},
		{"use", service.Use},
		{"want", service.Wants},
		{"after", service.After},
	} {
		for _, dep := range dependencies(group.field) {
			depend += "\t" + group.keyword + " " + dep + "\n"
		}
	}
	if depend != "" {
		text += "\ndepend() {\n" + depend + "}\n"
	}

	return text
}
