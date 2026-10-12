package setup

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// unitEscape stops systemd from expanding % and $ in paths or ending the quoted argument early.
func unitEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%", "$", "$$").Replace(s)
}

// Unit pins PATH because systemd user services start with a minimal PATH that
// usually lacks act and docker. It carries no secrets, so the file can be 0644.
func Unit(exe, cfgPath, path string) string {
	return fmt.Sprintf(`[Unit]
Description=homerun local CI
After=network-online.target docker.service

[Service]
ExecStart="%s" daemon --config "%s"
Environment=PATH=%s
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`, unitEscape(exe), unitEscape(cfgPath), strings.ReplaceAll(path, "%", "%%"))
}

func InstallSystemd(cfgPath string, out io.Writer) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	p := filepath.Join(dir, "systemd", "user", "homerun.service")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(Unit(exe, cfgPath, os.Getenv("PATH"))), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", "homerun.service"}} {
		if b, err := exec.Command("systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("systemctl %v: %w: %s", args, err, b)
		}
	}
	fmt.Fprintf(out, `Installed and started %s
Logs: journalctl --user -u homerun -f
To keep it running while you are logged out: loginctl enable-linger %s
`, p, os.Getenv("USER"))
	return nil
}
