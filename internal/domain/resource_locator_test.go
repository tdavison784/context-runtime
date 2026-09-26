package domain

import "testing"

func TestResourceLocatorIsScopedAndLexical(t *testing.T) {
	r := ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: "a.go"}
	want, _ := r.Key()
	for _, name := range []string{"a.go", "./a.go", "src/../a.go"} {
		r.Path = name
		got, err := r.Key()
		if err != nil || got != want {
			t.Fatalf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"../a.go", "/a.go", "a\\b", "a\nb", "C:/a"} {
		r.Path = name
		if _, err := r.Key(); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	r.Path = "a.go"
	r.ResourceID = "other"
	got, _ := r.Key()
	if got == want {
		t.Fatal("cross-resource alias")
	}
	r.ResourceID = "repo"
	r.BaseDir = "src"
	got, _ = r.Key()
	if got == want {
		t.Fatal("base directory omitted")
	}
}
