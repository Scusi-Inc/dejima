package selfupdate

import (
	"reflect"
	"testing"
)

func TestInstallMetaRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Missing file → zero value, no error.
	got, err := LoadInstallMeta()
	if err != nil || (got != InstallMeta{}) {
		t.Fatalf("missing meta = (%+v,%v), want (zero,nil)", got, err)
	}

	want := InstallMeta{SourceDir: "/home/me/dejima", System: true}
	if err := SaveInstallMeta(want); err != nil {
		t.Fatal(err)
	}
	got, err = LoadInstallMeta()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip = %+v, want %+v", got, want)
	}
}

func TestRestartArgs(t *testing.T) {
	if got := (InstallMeta{System: false}).RestartArgs(); !reflect.DeepEqual(got, []string{"service", "restart"}) {
		t.Errorf("user restart args = %v", got)
	}
	if got := (InstallMeta{System: true}).RestartArgs(); !reflect.DeepEqual(got, []string{"service", "restart", "--system"}) {
		t.Errorf("system restart args = %v", got)
	}
}
