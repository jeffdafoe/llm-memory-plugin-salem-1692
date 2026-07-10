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

func TestMemoryPartition(t *testing.T) {
	cases := []struct {
		name        string
		kind        ActorKind
		displayName string
		wantPrefix  string
		wantHas     bool
	}{
		{"dedicated VA owns its namespace", KindNPCStateful, "Josiah Thorne", "", true},
		{"shared VA sections by name", KindNPCShared, "Anne Walker", "anne-walker/", true},
		{"shared VA with punctuated name", KindNPCShared, "O'Brien the Elder", "o-brien-the-elder/", true},
		{"shared VA with unslugifiable name has no partition", KindNPCShared, "!!!", "", false},
		{"shared VA with empty name has no partition", KindNPCShared, "", "", false},
		{"PC has no memory", KindPC, "Jeff", "", false},
		{"decorative has no memory", KindDecorative, "Villager", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prefix, has := MemoryPartition(tc.kind, tc.displayName)
			if prefix != tc.wantPrefix || has != tc.wantHas {
				t.Fatalf("MemoryPartition(%d, %q) = (%q, %v), want (%q, %v)",
					tc.kind, tc.displayName, prefix, has, tc.wantPrefix, tc.wantHas)
			}
		})
	}
}

func TestSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Anne Walker", "anne-walker"},
		{"The Blacksmith's Name", "the-blacksmith-s-name"},
		{"  Trim  Me  ", "trim-me"},
		{"Multiple---Hyphens", "multiple-hyphens"},
		{"café münchen", "caf-m-nchen"}, // non-ASCII letters are not alphanumeric-ASCII → hyphenated
		{"!!!", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := Slugify(tc.in); got != tc.want {
			t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
