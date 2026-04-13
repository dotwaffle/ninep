package fstest

import "testing"

// TestCasesPopulated verifies that the Cases slice has been populated
// by the init function in cases.go. At least 10 test cases are expected
// covering walk, read, write, readdir, create, error, and concurrency.
func TestCasesPopulated(t *testing.T) {
	t.Parallel()
	if len(Cases) < 10 {
		t.Fatalf("len(Cases) = %d, want >= 10", len(Cases))
	}

	// Verify that key test categories are represented.
	categories := map[string]bool{
		"walk":       false,
		"read":       false,
		"write":      false,
		"readdir":    false,
		"create":     false,
		"error":      false,
		"concurrent": false,
	}
	for _, tc := range Cases {
		for cat := range categories {
			if len(tc.Name) >= len(cat) && tc.Name[:len(cat)] == cat {
				categories[cat] = true
			}
		}
	}
	for cat, found := range categories {
		if !found {
			t.Errorf("no test case found for category %q", cat)
		}
	}
}
