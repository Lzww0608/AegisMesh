package status

import "testing"

func TestParseAndStringRoundTrip(t *testing.T) {
	cases := []struct {
		input  string
		want   Code
		string string
	}{
		{"", Healthy, "HEALTHY"},
		{"HEALTHY", Healthy, "HEALTHY"},
		{"DEGRADED", Degraded, "DEGRADED"},
		{"EJECTED", Ejected, "EJECTED"},
		{"PROBING", Probing, "PROBING"},
		{"DEAD", Dead, "DEAD"},
		{"UNAVAILABLE", Unavailable, "UNAVAILABLE"},
	}
	for _, tc := range cases {
		if got := Parse(tc.input); got != tc.want {
			t.Fatalf("Parse(%q) = %d, want %d", tc.input, got, tc.want)
		}
		if got := tc.want.String(); got != tc.string {
			t.Fatalf("Code(%d).String() = %q, want %q", tc.want, got, tc.string)
		}
	}
}

func TestRoutableAndNormalTraffic(t *testing.T) {
	if !Healthy.Routable() || !Degraded.Routable() || !Probing.Routable() {
		t.Fatalf("expected healthy/degraded/probing to be routable")
	}
	if Ejected.Routable() || Dead.Routable() || Unavailable.Routable() {
		t.Fatalf("expected ejected/dead/unavailable to be non-routable")
	}
	if !Healthy.NormalTraffic() || !Degraded.NormalTraffic() {
		t.Fatalf("expected healthy/degraded in normal traffic")
	}
	if Probing.NormalTraffic() || Ejected.NormalTraffic() {
		t.Fatalf("expected probing/ejected outside normal traffic")
	}
}
