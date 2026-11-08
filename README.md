# go-openrc

Create and manage OpenRC services from Go. Generates `/etc/init.d/` scripts and
drives `rc-service` / `rc-update`.

```sh
go get github.com/alirezasn3/go-openrc
```

The API mirrors [go-systemd](https://github.com/alirezasn3/go-systemd), so a
program can switch between the two by changing its import.

## Usage

```go
import goOpenRC "github.com/alirezasn3/go-openrc"

execPath, _ := os.Executable()

// writes /etc/init.d/my-app, adds it to the default runlevel and starts it
err := goOpenRC.CreateService(&goOpenRC.Service{
    Name:       "my-app",
    ExecStart:  execPath,
    Restart:    "on-failure",
    RestartSec: "5s",
})

status, err := goOpenRC.StatusService("my-app")
if status.Exists && !status.Active {
    err = goOpenRC.StartService("my-app")
}

err = goOpenRC.DeleteService("my-app")
```

## API

| Function | Does |
| --- | --- |
| `CreateService(*Service) error` | Write init script, `rc-update add`, `rc-service start` |
| `DeleteService(name string) error` | `rc-service stop`, `rc-update delete` per runlevel, remove script |
| `StartService(name string) error` | `rc-service start` |
| `StopService(name string) error` | `rc-service stop` |
| `RestartService(name string) error` | `rc-service restart` |
| `StatusService(name string) (*Status, error)` | `rc-service status` + `rc-update show` |
| `DaemonReload() error` | `rc-update -u`, rebuilds the dependency cache |
| `Available() bool` | Whether `rc-service` and `rc-update` are installed |
| `(*Service) InitScript() string` | Render the script without writing it |

A missing service is not an error — `StatusService` returns `Exists: false`.
Start/Stop/Restart return `ErrServiceNotFound`.

### Service

`Name` and `ExecStart` are required; the `.service` suffix on a name is
optional. `Description` defaults to `Name`.

| Field | OpenRC |
| --- | --- |
| `ExecStart` | `command` (first field) + `command_args` (the rest) |
| `Restart` (anything but `""`/`no`) | `supervisor=supervise-daemon`, `respawn_max=0` |
| `Restart` unset | `command_background=true` |
| `RestartSec` | `respawn_delay`, rounded to whole seconds |
| `Description` | `description` |
| `Need` | `need` in `depend()` — hard dependency |
| `Use` | `use` in `depend()` — only if in the same runlevel |
| `Wants` | `want` in `depend()` |
| `After` | `after` in `depend()` |
| `Runlevel` | runlevel for `rc-update add` |
| `WantedBy` | runlevel, when `Runlevel` is unset |

`Need`, `Use` and `Runlevel` are OpenRC native. The rest are named after systemd
directives for drop-in compatibility: in dependency fields `network.target` and
`network-online.target` become `net` and `syslog` becomes `logger`, while any
other name is used as-is with its `.target`/`.service` suffix dropped;
`multi-user.target` and `graphical.target` map to the `default` runlevel.

Values are single-quoted in the generated script, so they cannot inject shell,
and newlines in any field are rejected.

### Status

`Name`, `Exists`, `Active`, `Enabled`, `State` (`started`, `stopped`,
`crashed`, …), `Runlevels`, `MainPID`, and `Raw`.

`MainPID` is read from `/run/<name>.pid`, which is where the generated scripts
put it; it stays `0` for services installed by something else.

### Errors

`ErrServiceNotFound`, `ErrPermissionDenied`, `ErrOpenRCNotFound`,
`ErrUnsupportedPlatform` — all comparable with `errors.Is`.

## Generated script

```sh
#!/sbin/openrc-run

description='my-app'
command='/usr/bin/my-app'
pidfile='/run/my-app.pid'
supervisor=supervise-daemon
respawn_max=0
respawn_delay=5
output_log='/var/log/my-app.log'
error_log='/var/log/my-app.log'
```

## Differences from systemd

- **Restart modes are not distinguished.** `on-failure`, `always`,
  `on-abnormal` and the rest all become plain supervision with unlimited
  respawns, because OpenRC has no equivalent distinction.
- **Logs go to a file.** systemd sends output to the journal; the generated
  scripts redirect to `/var/log/<name>.log`, which is not rotated.
- **`DaemonReload` is not a reload.** OpenRC has no `daemon-reload`; it runs
  `rc-update -u` to rebuild the dependency cache.

## Notes

- Linux only. On darwin and windows every function that touches the system
  returns `ErrUnsupportedPlatform`, so your code still builds and
  cross-compiles. `InitScript()` works everywhere.
- Installing a service needs root.

## License

MIT
