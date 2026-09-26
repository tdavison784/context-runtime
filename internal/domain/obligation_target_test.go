package domain

import "testing"

func TestObligationTargetModesAndClone(t *testing.T) {
	x := TargetSpec{File: &FileTarget{Locator: ResourceLocator{ResourceID: "r", BaseDir: ".", Path: "a"}, Mode: FileCurrentContent}}
	h, err := x.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	y := x.Clone()
	y.File.Mode = FileFixedHash
	y.File.RequiredHash = HashBytes(nil)
	h2, err := y.CanonicalHash()
	if err != nil || h == h2 || x.File.Mode != FileCurrentContent {
		t.Fatal("target mode identity/clone failure", err)
	}
	y.File.RequiredHash = ""
	if y.Validate() == nil {
		t.Fatal("unbound fixed hash accepted")
	}
}
