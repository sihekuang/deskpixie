package compat

import "testing"

func TestTheLabelNamesEverySupportedVersion(t *testing.T) {
	if got := Describe(Supported); got != SupportedVersions {
		t.Fatalf("SupportedVersions %q is out of date with the table: %q", SupportedVersions, got)
	}
	if SupportedVersions != "Herdr 0.7.5 (protocol 17) or 0.9.x (protocol 22)" {
		t.Fatal(SupportedVersions)
	}
}

func TestOnlyTheListedProtocolsAreSupported(t *testing.T) {
	for _, p := range []uint32{17, 22} {
		if !IsSupported(p) {
			t.Fatalf("%d", p)
		}
	}
	for _, p := range []uint32{0, 16, 18, 21, 23, 99} {
		if IsSupported(p) {
			t.Fatalf("%d", p)
		}
	}
	if v, ok := Find(22); !ok || v.Release != "0.9.x" {
		t.Fatalf("%+v %v", v, ok)
	}
}

func TestDescribeJoinsWithOr(t *testing.T) {
	cases := []struct {
		in   []Version
		want string
	}{
		{nil, ""},
		{[]Version{{1, "1.0"}}, "Herdr 1.0 (protocol 1)"},
		{[]Version{{1, "1.0"}, {2, "2.x"}, {3, "3.x"}}, "Herdr 1.0 (protocol 1), 2.x (protocol 2) or 3.x (protocol 3)"},
	}
	for _, c := range cases {
		if got := Describe(c.in); got != c.want {
			t.Fatalf("%v: %q", c.in, got)
		}
	}
}
