package alertlens_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestE2EDeployUsesHolmesPodPort(t *testing.T) {
	dir := t.TempDir()
	for name, script := range map[string]string{
		"kubectl": "#!/bin/sh\nprintf '%s' placeholder\n",
		"helm":    "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$DELIVERY_HELM_ARGS\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	argsFile := filepath.Join(dir, "helm-args")
	command := exec.Command("make", "--no-print-directory", "e2e-deploy")
	command.Env = []string{
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"DELIVERY_HELM_ARGS=" + argsFile,
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("make e2e-deploy: %v\n%s", err, output)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(string(data), "\n")
	for _, want := range []string{
		"holmesURL=http://holmes-holmes.holmes.svc:80",
		"networkPolicy.internalEgress[1].ports[0]=5050",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("Helm arguments omit %q: %q", want, args)
		}
	}
}
