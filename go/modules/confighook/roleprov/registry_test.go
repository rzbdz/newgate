package roleprov

import "testing"

func TestInstallDefaultRestoresOnlyItsOwnRegistry(t *testing.T) {
	original := currentRegistry()
	first := NewRegistry()
	restoreFirst := InstallDefault(first)
	second := NewRegistry()
	restoreSecond := InstallDefault(second)
	restoreFirst()
	if currentRegistry() != second {
		t.Fatal("stale restore replaced the current registry")
	}
	restoreSecond()
	if currentRegistry() != first {
		t.Fatal("nested registry did not restore its predecessor")
	}
	restoreFirst()
	if currentRegistry() != original {
		t.Fatal("original registry was not restored")
	}
}
