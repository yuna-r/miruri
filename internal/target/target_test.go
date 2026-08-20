package target

import "testing"

func TestBuiltInProfilesAreUnique(t *testing.T) {
	profiles, err := List()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, profile := range profiles {
		if profile.ID == "" || profile.Triple == "" || profile.OS == "" || profile.Arch == "" {
			t.Fatalf("incomplete profile: %+v", profile)
		}
		if seen[profile.ID] {
			t.Fatalf("duplicate profile %s", profile.ID)
		}
		seen[profile.ID] = true
	}
}
