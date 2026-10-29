package display

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tfyl/jkvm/internal/kvm/kvmtest"
)

// defaultEDID is the start of the firmware's default EDID: header, then a
// first detailed timing for 1920x1080 at byte 54.
const defaultEDID = "00FFFFFFFFFFFF0028B4010001EEFFC0302301038047287856EE91A3544C99260F5054000000D1C081C0318001010101010101010101023A801871382D40582C4500C48E2100001E" +
	"773300A050D02B2030203500122C2100001A000000FD00174C0F5111000A202020202020000000FC004A65744B564D2076310A20202001D5"

const otherEDID = "00FFFFFFFFFFFF00047265058A3F6101101E0104A53420783FC125A8554EA0260D5054BFEF80714F8140818081C081008B009500B300283C80A070B023403020360006442100001A" +
	"000000FD00304C575716010A202020202020000000FC0042323436574C0A202020202020000000FF0054384E4545303033383532320A01F8"

func fakeDevice() *kvmtest.Device {
	return kvmtest.New(map[string]string{
		"getEDIDPresets": `[{"name":"JetKVM Default","edid":"` + defaultEDID + `"},{"name":"Acer B246WL, 1920x1200","edid":"` + otherEDID + `"},{"name":"DELL IDRAC EDID, 1280x1024","edid":"00FF"}]`,
		"getEDID":        `"` + strings.ToLower(defaultEDID) + `"`,
		"getVideoState":  `{"ready":true,"streaming":0,"width":1920,"height":1080,"fps":60}`,
		"setEDID":        `null`,
	})
}

func TestPreferredMode(t *testing.T) {
	for _, c := range []struct {
		edid string
		w, h int
	}{
		{defaultEDID, 1920, 1080},
		{otherEDID, 1920, 1200},
		{"00ff", 0, 0},
		{"zz", 0, 0},
	} {
		if w, h := PreferredMode(c.edid); w != c.w || h != c.h {
			t.Errorf("PreferredMode(%.20s...) = %dx%d, want %dx%d", c.edid, w, h, c.w, c.h)
		}
	}
}

func TestStatusMatchesPresetIgnoringCase(t *testing.T) {
	st, err := GetStatus(context.Background(), fakeDevice())
	if err != nil {
		t.Fatal(err)
	}
	if st.Preset != DefaultPreset || st.Width != 1920 || st.Height != 1080 || st.FPS != 60 || !st.Ready {
		t.Errorf("status %+v", st)
	}
}

func TestPresets(t *testing.T) {
	ps, err := Presets(context.Background(), fakeDevice())
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 3 || ps[1].Width != 1920 || ps[1].Height != 1200 || ps[2].Width != 1280 || ps[2].Height != 1024 {
		t.Errorf("presets %+v", ps)
	}
}

func TestResolve(t *testing.T) {
	ps, _ := Presets(context.Background(), fakeDevice())
	if got, err := Resolve(ps, "acer b246wl, 1920x1200"); err != nil || got != otherEDID {
		t.Errorf("by name: %v", err)
	}
	if got, err := Resolve(ps, " "+strings.ToLower(otherEDID)+"\n"); err != nil || got != strings.ToLower(otherEDID) {
		t.Errorf("by hex: %q %v", got, err)
	}
	for _, bad := range []string{"Acer", "00ff", strings.Repeat("zz", 128)} {
		if _, err := Resolve(ps, bad); !errors.Is(err, ErrBadEDID) {
			t.Errorf("Resolve(%.10q) = %v, want ErrBadEDID", bad, err)
		}
	}
}

func TestSetWaitsForVideo(t *testing.T) {
	d := fakeDevice()
	v, err := Set(context.Background(), d, otherEDID, SetOptions{Settle: time.Millisecond, Interval: time.Millisecond})
	if err != nil || !v.Ready || v.Width != 1920 {
		t.Fatalf("Set = %+v, %v", v, err)
	}
	calls := d.Calls()
	if calls[0].Method != "setEDID" || calls[0].Params["edid"] != otherEDID || calls[1].Method != "getVideoState" {
		t.Errorf("calls %+v", calls)
	}
}

func TestSetTimesOutWithoutVideo(t *testing.T) {
	d := fakeDevice()
	d.Results["getVideoState"] = []byte(`{"ready":false,"width":0,"height":0}`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := Set(ctx, d, otherEDID, SetOptions{Settle: time.Millisecond, Interval: 5 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "EDID set") {
		t.Errorf("err %v", err)
	}
}
