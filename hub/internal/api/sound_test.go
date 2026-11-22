package api

import (
	"encoding/binary"
	"testing"
)

func TestWAVDurationIgnoresDeclaredByteRate(t *testing.T) {
	b := wavBytes(31)
	binary.LittleEndian.PutUint32(b[28:32], 0xFFFFFFFF) // fmt byte_rate: forged to make the clip look instant
	d, err := wavDuration(b)
	if err != nil || d < maxSoundDuration {
		t.Fatalf("duration %v, err %v: a forged byte_rate must not hide a 31 s clip", d, err)
	}
}

func TestAIFFOversizedCOMMDoesNotPanic(t *testing.T) {
	b := aiffBytes(8000, 8000)
	binary.BigEndian.PutUint32(b[16:20], 0x7fffffff) // COMM chunk size far past the end of the file
	if d, err := aiffDuration(b); err == nil && d <= 0 {
		t.Fatalf("duration %v", d)
	}
}
