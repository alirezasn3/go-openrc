package goOpenRC

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestServiceName(t *testing.T) {
	for in, want := range map[string]string{"foo": "foo", "foo.service": "foo", " foo ": "foo"} {
		if got := serviceName(in); got != want {
			t.Errorf("serviceName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidate(t *testing.T) {
	for _, test := range []struct {
		name    string
		service *Service
		wantErr bool
	}{
		{"empty", &Service{}, true},
		{"no ExecStart", &Service{Name: "a"}, true},
		{"whitespace ExecStart", &Service{Name: "a", ExecStart: "  "}, true},
		{"path separator in name", &Service{Name: "../evil", ExecStart: "/bin/x"}, true},
		{"newline injection", &Service{Name: "a", ExecStart: "/bin/x\ncommand_user=root"}, true},
		{"valid", &Service{Name: "a", ExecStart: "/bin/x"}, false},
	} {
		e := test.service.validate()
		if (e != nil) != test.wantErr {
			t.Errorf("%s: validate() error = %v, wantErr %v", test.name, e, test.wantErr)
		}
	}
}

func TestSeconds(t *testing.T) {
	for in, want := range map[string]int{"": 0, "5": 5, "5s": 5, "1min": 60, "2m": 120, "500ms": 1, "bogus": 0} {
		if got := seconds(in); got != want {
			t.Errorf("seconds(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestRunlevel(t *testing.T) {
	for _, test := range []struct {
		service *Service
		want    string
	}{
		{&Service{}, "default"},
		{&Service{WantedBy: "multi-user.target"}, "default"},
		{&Service{WantedBy: "graphical.target"}, "default"},
		{&Service{WantedBy: "sysinit.target"}, "sysinit"},
		{&Service{WantedBy: "multi-user.target", Runlevel: "boot"}, "boot"},
	} {
		if got := test.service.runlevel(); got != test.want {
			t.Errorf("runlevel(%+v) = %q, want %q", test.service, got, test.want)
		}
	}
}

func TestDependencies(t *testing.T) {
	got := strings.Join(dependencies("network.target syslog.service dbus.service"), " ")
	if want := "net logger dbus"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := dependencies(""); got != nil {
		t.Errorf("empty field should give no dependencies, got %v", got)
	}
}

func TestParseState(t *testing.T) {
	for in, want := range map[string]string{
		" * status: started\n": "started",
		" * status: stopped\n": "stopped",
		" * status: crashed\n": "crashed",
		"":                     "",
	} {
		if got := parseState(in); got != want {
			t.Errorf("parseState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShQuoteIsInjectionSafe(t *testing.T) {
	// a single quote in a value must not break out of the quoted string
	nasty := `a'; rm -rf /; echo '`
	script := "#!/bin/sh\nx=" + shQuote(nasty) + "\nprintf '%s' \"$x\"\n"
	if e := shSyntaxCheck(t, script); e != nil {
		t.Fatalf("quoting produced invalid shell: %v", e)
	}
	// the shell must read back exactly what went in
	out, e := exec.Command("sh", "-c", script).Output()
	if e != nil {
		t.Fatalf("running the script failed: %v", e)
	}
	if string(out) != nasty {
		t.Errorf("value round tripped as %q, want %q", out, nasty)
	}
}

func TestInitScriptSupervised(t *testing.T) {
	service := &Service{
		Name: "my-app", ExecStart: "/usr/bin/my-app --port 8080",
		Restart: "on-failure", RestartSec: "5s",
		After: "network.target", Need: "net", Use: "logger",
	}
	if e := service.validate(); e != nil {
		t.Fatal(e)
	}
	script := service.InitScript()
	t.Logf("\n%s", script)
	if e := shSyntaxCheck(t, script); e != nil {
		t.Fatalf("invalid shell: %v", e)
	}
	for _, want := range []string{
		"#!/sbin/openrc-run",
		"command='/usr/bin/my-app'",
		"command_args='--port 8080'",
		"pidfile='/run/my-app.pid'",
		"supervisor=supervise-daemon",
		"respawn_max=0",
		"respawn_delay=5",
		"\tneed net\n",
		"\tuse logger\n",
		"\tafter net\n",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("missing %q", want)
		}
	}
	// supervise-daemon wants the daemon in the foreground
	if strings.Contains(script, "command_background") {
		t.Error("command_background must not be set alongside supervise-daemon")
	}
}

func TestInitScriptUnsupervised(t *testing.T) {
	service := &Service{Name: "plain.service", ExecStart: "/usr/bin/plain"}
	script := service.InitScript()
	t.Logf("\n%s", script)
	if e := shSyntaxCheck(t, script); e != nil {
		t.Fatalf("invalid shell: %v", e)
	}
	if !strings.Contains(script, "command_background=true") {
		t.Error("an unsupervised service needs command_background")
	}
	for _, unwanted := range []string{"supervisor", "respawn", "command_args", "depend()"} {
		if strings.Contains(script, unwanted) {
			t.Errorf("unexpected %q", unwanted)
		}
	}
	if !strings.Contains(script, "description='plain'") {
		t.Error("description should default to the service name")
	}
}

// shSyntaxCheck runs `sh -n` over the script to confirm it parses as shell.
func shSyntaxCheck(t *testing.T, script string) error {
	t.Helper()
	path := t.TempDir() + "/script.sh"
	if e := os.WriteFile(path, []byte(script), 0644); e != nil {
		return e
	}
	if out, e := exec.Command("sh", "-n", path).CombinedOutput(); e != nil {
		if len(out) > 0 {
			return &shError{string(out)}
		}
		return e
	}
	return nil
}

type shError struct{ out string }

func (e *shError) Error() string { return e.out }
