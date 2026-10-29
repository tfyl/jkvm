package media

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/tfyl/jkvm/internal/kvm/kvmtest"
)

func TestMountURLRejectsUnusableURL(t *testing.T) {
	d := kvmtest.New(map[string]string{
		"getVirtualMediaState": "null",
		"checkMountUrl":        `{"usable":false,"reason":"HTTP 404"}`,
		"mountWithHTTP":        "null",
	})
	err := MountURL(context.Background(), d, "http://example.com/x.iso", ModeCDROM)
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range d.Calls() {
		if c.Method == "mountWithHTTP" {
			t.Error("mounted an unusable URL")
		}
	}
}

func TestMountRefusesWhenMounted(t *testing.T) {
	d := kvmtest.New(map[string]string{"getVirtualMediaState": `{"source":"HTTP"}`})
	err := MountURL(context.Background(), d, "http://example.com/x.iso", ModeCDROM)
	if err == nil || !strings.Contains(err.Error(), "already mounted") {
		t.Fatalf("err = %v", err)
	}
}

func TestMountFileSkipsUploadWhenStored(t *testing.T) {
	path := t.TempDir() + "/disk.img"
	if err := writeFile(path, 10); err != nil {
		t.Fatal(err)
	}
	d := kvmtest.New(map[string]string{
		"getVirtualMediaState": "null",
		"listStorageFiles":     `{"files":[{"filename":"disk.img","size":10}]}`,
		"mountWithStorage":     "null",
	})
	// Mounting flips getVirtualMediaState; the fake has no state, so verify
	// the failure message names the post-mount check rather than an upload.
	err := MountFile(context.Background(), d, path, ModeDisk, nil)
	if err == nil || !strings.Contains(err.Error(), "reports nothing mounted") {
		t.Fatalf("err = %v", err)
	}
	var mounted bool
	for _, c := range d.Calls() {
		switch c.Method {
		case "startStorageFileUpload":
			t.Error("uploaded a file already in storage")
		case "mountWithStorage":
			mounted = c.Params["filename"] == "disk.img" && c.Params["mode"] == "Disk"
		}
	}
	if !mounted {
		t.Error("mountWithStorage not called with disk.img in Disk mode")
	}
}

func TestMountFileRefusesSizeMismatch(t *testing.T) {
	path := t.TempDir() + "/disk.img"
	if err := writeFile(path, 10); err != nil {
		t.Fatal(err)
	}
	d := kvmtest.New(map[string]string{
		"getVirtualMediaState": "null",
		"listStorageFiles":     `{"files":[{"filename":"disk.img","size":7}]}`,
	})
	err := MountFile(context.Background(), d, path, ModeDisk, nil)
	if err == nil || !strings.Contains(err.Error(), "different size") {
		t.Fatalf("err = %v", err)
	}
}

func TestGatherReportsErrorsInline(t *testing.T) {
	d := kvmtest.New(map[string]string{"getDeviceID": `"abc"`})
	raw, err := gather(context.Background(), d, []string{"getDeviceID", "noSuchMethod"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if string(out["getDeviceID"]) != `"abc"` || !strings.Contains(string(out["noSuchMethod"]), "Method not found") {
		t.Errorf("got %s", raw)
	}
	if _, err := gather(context.Background(), d, []string{"noSuchMethod"}); err == nil {
		t.Error("want an error when every call fails")
	}
}

func writeFile(path string, size int) error {
	return os.WriteFile(path, make([]byte, size), 0o600)
}
