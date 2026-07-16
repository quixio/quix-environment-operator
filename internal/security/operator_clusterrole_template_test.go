package security

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOperatorClusterRoleTemplateRoleBindingVerbs(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve current test path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	templatePath := root + "/deploy/quix-environment-operator/templates/operator-cluster-role.yaml"

	content, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("failed to read operator ClusterRole template: %v", err)
	}

	text := string(content)
	ruleStart := strings.Index(text, `resources: ["rolebindings"]`)
	if ruleStart == -1 {
		t.Fatal("operator ClusterRole template must contain a rolebindings rule")
	}
	remaining := text[ruleStart:]
	verbsStart := strings.Index(remaining, `verbs: [`)
	if verbsStart == -1 {
		t.Fatal("rolebindings rule must declare verbs")
	}
	verbsLine := remaining[verbsStart:]
	if end := strings.IndexByte(verbsLine, '\n'); end != -1 {
		verbsLine = verbsLine[:end]
	}

	for _, verb := range []string{`"get"`, `"list"`, `"create"`, `"update"`, `"delete"`, `"watch"`} {
		if !strings.Contains(verbsLine, verb) {
			t.Fatalf("rolebindings rule missing required verb %s in %s", verb, verbsLine)
		}
	}
}
