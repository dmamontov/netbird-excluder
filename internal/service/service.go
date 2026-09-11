package service

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const launchDaemonsDir = "/Library/LaunchDaemons"

func plistPath(label string) string {
	return filepath.Join(launchDaemonsDir, label+".plist")
}

func logPath(label string) string {
	return filepath.Join("/var/log", label+".log")
}

func Install(label, binPath string, args []string) error {
	if err := installBinary(binPath); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}

	plistArgs := append([]string{binPath}, args...)
	content := renderPlist(label, plistArgs, logPath(label))

	path := plistPath(label)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("write plist %s: %w", path, err)
	}

	_ = exec.Command("launchctl", "unload", "-w", path).Run()
	if out, err := exec.Command("launchctl", "load", "-w", path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl load: %w: %s", err, out)
	}
	return nil
}

func Uninstall(label string) error {
	path := plistPath(label)
	_ = exec.Command("launchctl", "unload", "-w", path).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist %s: %w", path, err)
	}
	return nil
}

func installBinary(binPath string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return err
	}
	if self == binPath {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(binPath), 0755); err != nil {
		return err
	}

	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()

	tmp := binPath + ".tmp"
	dst, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(tmp)
		return err
	}
	if err := dst.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, binPath)
}

func renderPlist(label string, args []string, logFile string) string {
	argsXML := ""
	for _, a := range args {
		argsXML += "\t\t<string>" + xmlEscape(a) + "</string>\n"
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + xmlEscape(label) + `</string>
	<key>ProgramArguments</key>
	<array>
` + argsXML + `	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>` + xmlEscape(logFile) + `</string>
	<key>StandardErrorPath</key>
	<string>` + xmlEscape(logFile) + `</string>
</dict>
</plist>
`
}

func xmlEscape(s string) string {
	var buf []byte
	if err := xml.EscapeText(byteWriter{&buf}, []byte(s)); err != nil {
		return s
	}
	return string(buf)
}

type byteWriter struct{ buf *[]byte }

func (w byteWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}
