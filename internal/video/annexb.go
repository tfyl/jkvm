package video

// H.264 NAL unit types we care about.
const (
	nalSlice = 1
	nalIDR   = 5
	nalSPS   = 7
	nalPPS   = 8
)

var startCode = []byte{0, 0, 0, 1}

// splitNALs returns the NAL units of an Annex-B byte stream, without start
// codes. Both 3- and 4-byte start codes are accepted.
func splitNALs(b []byte) [][]byte {
	var nals [][]byte
	start := -1
	for i := 0; i+2 < len(b); {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if start >= 0 {
				end := i
				if end > start && b[end-1] == 0 {
					end-- // 4-byte start code
				}
				nals = append(nals, b[start:end])
			}
			i += 3
			start = i
			continue
		}
		i++
	}
	if start >= 0 && start < len(b) {
		nals = append(nals, b[start:])
	}
	return nals
}

func nalType(nal []byte) byte {
	if len(nal) == 0 {
		return 0
	}
	return nal[0] & 0x1f
}

// accessUnit summarises one depacketised sample.
type accessUnit struct {
	idr      bool
	sps, pps []byte // last SPS/PPS carried in-band, if any
}

func inspect(annexB []byte) accessUnit {
	var au accessUnit
	for _, n := range splitNALs(annexB) {
		switch nalType(n) {
		case nalIDR:
			au.idr = true
		case nalSPS:
			au.sps = n
		case nalPPS:
			au.pps = n
		}
	}
	return au
}

// appendNAL appends one NAL unit with a 4-byte start code.
func appendNAL(dst, nal []byte) []byte {
	return append(append(dst, startCode...), nal...)
}
