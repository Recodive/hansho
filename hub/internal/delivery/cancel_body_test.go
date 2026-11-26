package delivery

import (
	"testing"

	"github.com/recodive/hansho/hub/internal/store"
)

func TestCancelBodyNamesTheAckerByDisplayNameNeverById(t *testing.T) {
	m := store.Message{ExternalUserID: "bob", AckedBy: "alice@example.com"}
	if got := cancelBody("Disk full", m); got != "Disk full was acknowledged by another recipient" {
		t.Fatal(got)
	}
	m.AckedByName = "Álice 🔔"
	if got := cancelBody("Disk full", m); got != "Disk full was acknowledged by Álice 🔔" {
		t.Fatal(got)
	}
	m.AckedBy = "bob" // acknowledged by the person themselves
	if got := cancelBody("Disk full", m); got != "Disk full was acknowledged" {
		t.Fatal(got)
	}
}
