//go:build windows

package pasteimg

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const supported = true

// capture pulls a PNG off the Windows clipboard via Windows PowerShell (5.1,
// built in) + System.Windows.Forms — no third-party tool. Clipboard access needs
// an STA thread (-Sta). The script saves the clipboard image to a temp PNG, or
// exits 3 when the clipboard holds no image; we map exit 3 → ErrNoImage so the
// caller degrades with a friendly message rather than a raw PowerShell dump.
func capture() (Image, error) {
	tmp, err := os.CreateTemp("", "dejima-paste-*.png")
	if err != nil {
		return Image{}, err
	}
	path := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(path)

	script := "Add-Type -AssemblyName System.Windows.Forms,System.Drawing; " +
		"$img=[System.Windows.Forms.Clipboard]::GetImage(); " +
		"if($img -eq $null){ exit 3 }; " +
		"$img.Save('" + psSingleQuote(path) + "',[System.Drawing.Imaging.ImageFormat]::Png)"

	out, err := exec.Command("powershell", "-NoProfile", "-Sta", "-Command", script).CombinedOutput()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 3 {
			return Image{}, ErrNoImage
		}
		return Image{}, fmt.Errorf("powershell clipboard capture: %w: %s", err, strings.TrimSpace(string(out)))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Image{}, err
	}
	if len(b) == 0 {
		return Image{}, ErrNoImage
	}
	return Image{Bytes: b, Ext: "png"}, nil
}

// psSingleQuote escapes a path for a PowerShell single-quoted string literal:
// only the single quote needs doubling; backslashes are literal.
func psSingleQuote(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}
