package main

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestCheckRoots(t *testing.T) {
	approved, err := os.ReadFile("testdata/isrg-root-x1.pem")
	if err != nil {
		t.Fatal(err)
	}
	denied := map[string]bool{}
	for _, line := range strings.Split(string(read("../rejected-roots.sha256")), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			denied[line] = true
		}
	}
	chain := certs(read("../fixtures/dv-g3b-emudhra.we-have-a-problem.com.pem"))
	var roots []struct {
		PEM string `json:"pem"`
	}
	if err := json.Unmarshal(read("../fixtures/roots.json"), &roots); err != nil {
		t.Fatal(err)
	}
	var rejected []byte
	for _, root := range roots {
		rejected = append(rejected, []byte(root.PEM)...)
		rejected = append(rejected, '\n')
	}
	for _, tc := range []struct {
		name      string
		bundle    []byte
		wantPanic string
	}{
		{"approved root present", approved, ""},
		{"approved root absent", nil, "approved ISRG Root X1 absent"},
		{"unrelated certificates", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: chain[0].Raw}), "approved ISRG Root X1 absent"},
		{"rejected root present", append(append([]byte{}, approved...), rejected...), "denied root:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				got := recover()
				if tc.wantPanic == "" {
					if got != nil {
						t.Errorf("unexpected panic: %v", got)
					}
				} else if got == nil || !strings.Contains(fmt.Sprint(got), tc.wantPanic) {
					t.Errorf("panic = %v, want %q", got, tc.wantPanic)
				}
			}()
			if count := checkRoots(tc.bundle, denied); count != 1 {
				t.Errorf("root count = %d, want 1", count)
			}
		})
	}
}
