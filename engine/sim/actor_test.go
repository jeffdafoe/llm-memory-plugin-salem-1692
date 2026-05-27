package sim

import "testing"

func TestClassifyActorKind(t *testing.T) {
	cases := []struct {
		name          string
		loginUsername string
		llmAgent      string
		want          ActorKind
	}{
		{"human PC by login", "jeff", "", KindPC},
		{"shared VA vendor", "", VendorAgentName, KindNPCShared},
		{"shared VA visitor", "", VisitorAgentName, KindNPCShared},
		{"shared VA generic", "", GenericAgentName, KindNPCShared},
		{"own VA stateful", "", "zbbs-john-ellis", KindNPCStateful},
		{"sprite-only decorative", "", "", KindDecorative},
		{"whitespace login is not a driver", "   ", "", KindDecorative},
		{"whitespace agent is not a driver", "", "  ", KindDecorative},
		{"padded shared VA still classifies", "", " salem-vendor ", KindNPCShared},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ClassifyActorKind(tc.loginUsername, tc.llmAgent)
			if got != tc.want {
				t.Fatalf("ClassifyActorKind(%q, %q) = %d, want %d",
					tc.loginUsername, tc.llmAgent, got, tc.want)
			}
		})
	}
}
