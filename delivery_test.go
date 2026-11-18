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
	requirePOSIXShell(t)
	makeVersion := exec.Command("make", "--version")
	version, err := makeVersion.Output()
	requireScriptTool(t, "GNU Make available as make", err == nil && strings.HasPrefix(string(version), "GNU Make "))

	dir := t.TempDir()
	if strings.ContainsRune(dir, os.PathListSeparator) {
		t.Fatalf("stub directory %q contains the PATH separator %q", dir, os.PathListSeparator)
	}
	for name, script := range map[string]string{
		"kubectl": "#!/bin/sh\nprintf '%s' placeholder\n",
		"helm":    "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$DELIVERY_HELM_ARGS\"\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	argsFile := filepath.Join(dir, "helm-args")
	command := exec.Command(makeVersion.Path, "--no-print-directory", "e2e-deploy")
	command.Env = []string{
		"PATH=" + dir,
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

func requirePOSIXShell(t *testing.T) {
	t.Helper()
	_, err := exec.LookPath("/bin/sh")
	requireScriptTool(t, "/bin/sh", err == nil)
}

func requireScriptTool(t *testing.T, tool string, available bool) {
	t.Helper()
	if !available {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatalf("required tool unavailable: %s", tool)
		}
		t.Skipf("required tool unavailable: %s", tool)
	}
}
