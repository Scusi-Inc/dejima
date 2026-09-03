package wsl

import (
	"strings"
	"testing"
	"unicode/utf16"
)

func utf16le(s string) []byte {
	var b []byte
	for _, r := range utf16.Encode([]rune(s)) {
		b = append(b, byte(r), byte(r>>8))
	}
	return b
}

// wsl.exe writes some failures to STDOUT — which is the tunnel — so its error
// text arrives where the HTTP response should be. The operator's dashboard
// filled with pages of it and `dejima logs` failed with
//
//	malformed HTTP status code "\x00o\x00p\x00e\x00r\x00a\x00t\x00i\x00o\x00n"
//
// rather than with the actual fault, which was WSL running out of socket
// resources.
func TestUTF16OnTheTunnelBecomesAnError(t *testing.T) {
	raw := utf16le("An operation on a socket could not be performed because the system " +
		"lacked sufficient buffer space or because a queue was full. \r\n" +
		"Error code: Wsl/Service/0x80072747\r\n")

	// The discriminator, exactly as Read applies it.
	if len(raw) < 2 || raw[1] != 0x00 || raw[0] == 0x00 {
		t.Fatal("the UTF-16 discriminator does not match real wsl.exe output")
	}

	got := wslServiceHint(strings.Join(strings.Fields(decodeWSLText(raw)), " "))

	if !strings.Contains(got, "socket") {
		t.Errorf("the decoded message lost its meaning:\n%s", got)
	}
	// The condition alone is not actionable. The remedy has to be there.
	if !strings.Contains(got, "wsl --shutdown") {
		t.Errorf("names the fault but not the fix:\n%s", got)
	}
}

// A REAL dejimad response must never be mistaken for wsl.exe error text. "HTTP"
// has 'T' as its second byte; UTF-16LE has a NUL. If that ever stops being true
// the tunnel would report every successful call as a WSL failure.
func TestHTTPResponsesAreNotMistakenForWSLErrors(t *testing.T) {
	for _, resp := range []string{
		"HTTP/1.1 200 OK\r\n\r\n",
		"HTTP/1.1 404 Not Found\r\n\r\n",
		"HTTP/1.0 500 Internal Server Error\r\n\r\n",
	} {
		b := []byte(resp)
		if len(b) >= 2 && b[1] == 0x00 && b[0] != 0x00 {
			t.Errorf("a normal response %q would be misread as wsl.exe UTF-16 output", resp)
		}
	}
}

// Only the WSAENOBUFS case gets the shutdown advice; anything else is passed
// through rather than given a remedy that may not apply. Handing out a wrong
// fix is the failure this repo has spent the week removing.
func TestHintOnlyFiresForTheFaultItKnows(t *testing.T) {
	other := "Wsl/Service/0x8007019e The Windows Subsystem for Linux has no installed distributions."
	if strings.Contains(wslServiceHint(other), "wsl --shutdown") {
		t.Errorf("offered the socket-exhaustion remedy for an unrelated fault:\n%s", wslServiceHint(other))
	}
}
