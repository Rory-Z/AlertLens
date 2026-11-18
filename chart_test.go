package alertlens_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/emqx/alertlens/internal/scheduled"
	"go.yaml.in/yaml/v2"
)

func TestScheduledInvestigationsChartContract(t *testing.T) {
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("install Helm to test the chart configuration contract")
	}
	dir := t.TempDir()
	valuesFile := filepath.Join(dir, "values.yaml")
	if err := os.WriteFile(valuesFile, []byte(`slack:
  existingSecret: alertlens-slack
  alertChannel: C1
alertmanagerURL: http://alertmanager:9093
holmesURL: http://holmes:5050
scheduledInvestigations:
  - name: daily health
    schedule: "0 1 * * *"
    model: scheduled
    prompt: |
      Investigate {{ .Release.Name }} line one.
      Investigate line two.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(helm, "template", "contract", "charts/alertlens",
		"--show-only", "templates/configmap.yaml", "--values", valuesFile).Output()
	if err != nil {
		t.Fatalf("helm template: %v", err)
	}
	var configMap struct {
		Kind string            `yaml:"kind"`
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(output, &configMap); err != nil {
		t.Fatal(err)
	}
	if configMap.Kind != "ConfigMap" {
		t.Fatalf("rendered kind = %q", configMap.Kind)
	}
	path := filepath.Join(dir, "scheduled-investigations.yaml")
	if err := os.WriteFile(path, []byte(configMap.Data["scheduled-investigations.yaml"]), 0o600); err != nil {
		t.Fatal(err)
	}
	investigations, err := scheduled.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(investigations) != 1 {
		t.Fatalf("loaded %d investigations, want 1", len(investigations))
	}
	got := investigations[0]
	if got.Name != "daily health" || got.Schedule() != "0 1 * * *" || got.Model != "scheduled" ||
		got.Prompt != "Investigate {{ .Release.Name }} line one.\nInvestigate line two.\n" {
		t.Fatalf("loaded investigation = %#v", got)
	}
}
