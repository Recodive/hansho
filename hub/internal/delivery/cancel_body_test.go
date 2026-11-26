package delivery

import (
	"testing"

	"github.com/recodive/hansho/hub/internal/store"
)

func TestCancelBodyNamesTheAckerOnlyWhenAllowed(t *testing.T) {
	m := store.Message{ExternalUserID: "bob", AckedBy: "alice"}
	if got := cancelBody("Disk full", m); got != "Disk full was acknowledged by another recipient" {
		t.Fatal(got)
	}
	m.ShowAcker = true
	if got := cancelBody("Disk full", m); got != "Disk full was acknowledged by alice" {
		t.Fatal(got)
	}
	m.AckedBy = "bob" // acknowledged by the person themselves
	if got := cancelBody("Disk full", m); got != "Disk full was acknowledged" {
		t.Fatal(got)
	}
}
