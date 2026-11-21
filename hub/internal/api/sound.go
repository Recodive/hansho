package api

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"time"
)

// maxSoundDuration is the exclusive upper bound for WAV/AIFF sounds.
const maxSoundDuration = 30 * time.Second

var errBadAudio = errors.New("malformed audio header")

// wavDuration returns the playing time of a PCM WAV file from its header.
// The longer of the declared and the actual data length is used, so a header
// that understates the data cannot sneak a long sound through.
func wavDuration(b []byte) (time.Duration, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return 0, errBadAudio
	}
	var byteRate uint32
	pos := 12
	for pos+8 <= len(b) {
		id, size := string(b[pos:pos+4]), int(binary.LittleEndian.Uint32(b[pos+4:pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(b) {
				return 0, errBadAudio
			}
			byteRate = binary.LittleEndian.Uint32(b[body+8 : body+12])
		case "data":
			if byteRate == 0 {
				return 0, errBadAudio
			}
			n := max(size, len(b)-body)
			return time.Duration(float64(n) / float64(byteRate) * float64(time.Second)), nil
		}
		pos = body + size + size%2
		if size < 0 || pos < body {
			return 0, errBadAudio
		}
	}
	return 0, errBadAudio
}

// aiffDuration returns the playing time of an AIFF/AIFC file from its COMM chunk.
func aiffDuration(b []byte) (time.Duration, error) {
	if len(b) < 12 || string(b[0:4]) != "FORM" || (string(b[8:12]) != "AIFF" && string(b[8:12]) != "AIFC") {
		return 0, errBadAudio
	}
	pos := 12
	for pos+8 <= len(b) {
		id, size := string(b[pos:pos+4]), int(binary.BigEndian.Uint32(b[pos+4:pos+8]))
		body := pos + 8
		if id == "COMM" {
			if size < 18 || body+18 > len(b) {
				return 0, errBadAudio
			}
			channels := int(binary.BigEndian.Uint16(b[body : body+2]))
			frames := float64(binary.BigEndian.Uint32(b[body+2 : body+6]))
			bits := int(binary.BigEndian.Uint16(b[body+6 : body+8]))
			exp := int(binary.BigEndian.Uint16(b[body+8:body+10]) & 0x7fff)
			mant := binary.BigEndian.Uint64(b[body+10 : body+18])
			rate := math.Ldexp(float64(mant), exp-16383-63)
			if rate <= 0 || math.IsInf(rate, 0) || math.IsNaN(rate) || channels <= 0 || bits <= 0 {
				return 0, errBadAudio
			}
			// Also count the sample frames actually present in the SSND chunk.
			if i := bytes.Index(b[body+size:], []byte("SSND")); i >= 0 {
				p := body + size + i
				if p+8 <= len(b) {
					bytesPer := channels * ((bits + 7) / 8)
					actual := float64(max(len(b)-(p+16), 0) / bytesPer)
					frames = math.Max(frames, actual)
				}
			}
			return time.Duration(frames / rate * float64(time.Second)), nil
		}
		pos = body + size + size%2
		if size < 0 || pos < body {
			return 0, errBadAudio
		}
	}
	return 0, errBadAudio
}

// isCAF checks the CAF file magic and version. CAF duration is not checked.
func isCAF(b []byte) bool {
	return len(b) >= 8 && string(b[0:4]) == "caff" && b[4] == 0 && b[5] == 1
}
