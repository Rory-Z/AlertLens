package alertlens_test

import (
	"bytes"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v2"
)

type slackManifest struct {
	Metadata struct {
		MajorVersion int `yaml:"major_version"`
	} `yaml:"_metadata"`
	DisplayInformation struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	} `yaml:"display_information"`
	Features struct {
		BotUser struct {
			DisplayName  string `yaml:"display_name"`
			AlwaysOnline bool   `yaml:"always_online"`
		} `yaml:"bot_user"`
	} `yaml:"features"`
	OAuthConfig struct {
		Scopes struct {
			Bot []string `yaml:"bot"`
		} `yaml:"scopes"`
	} `yaml:"oauth_config"`
	Settings struct {
		EventSubscriptions struct {
			BotEvents []string `yaml:"bot_events"`
		} `yaml:"event_subscriptions"`
		OrgDeployEnabled     bool `yaml:"org_deploy_enabled"`
		SocketModeEnabled    bool `yaml:"socket_mode_enabled"`
		IsHosted             bool `yaml:"is_hosted"`
		TokenRotationEnabled bool `yaml:"token_rotation_enabled"`
	} `yaml:"settings"`
}

func TestSlackManifestEnvironments(t *testing.T) {
	wantNames := map[string]string{"dev": "AlertLens Dev", "prod": "AlertLens"}
	wantScopes := []string{
		"app_mentions:read", "channels:history", "groups:history", "chat:write",
		"reactions:read", "reactions:write",
	}
	wantEvents := []string{"app_mention", "message.channels", "message.groups"}
	var normalized *slackManifest

	for environment, wantName := range wantNames {
		t.Run(environment, func(t *testing.T) {
			output, err := exec.Command("./scripts/render-slack-manifest", environment).Output()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(output), "__ALERTLENS_APP_NAME__") {
				t.Fatalf("unrendered placeholder in %q", output)
			}
			var got slackManifest
			if err := yaml.UnmarshalStrict(output, &got); err != nil {
				t.Fatal(err)
			}
			if got.Metadata.MajorVersion != 1 ||
				got.DisplayInformation.Name != wantName ||
				got.Features.BotUser.DisplayName != wantName ||
				!reflect.DeepEqual(got.OAuthConfig.Scopes.Bot, wantScopes) ||
				!reflect.DeepEqual(got.Settings.EventSubscriptions.BotEvents, wantEvents) ||
				!got.Settings.SocketModeEnabled || got.Settings.OrgDeployEnabled ||
				got.Settings.IsHosted || got.Settings.TokenRotationEnabled {
				t.Fatalf("manifest = %#v", got)
			}

			got.DisplayInformation.Name = ""
			got.Features.BotUser.DisplayName = ""
			if normalized == nil {
				normalized = &got
			} else if !reflect.DeepEqual(*normalized, got) {
				t.Fatalf("environment manifests differ beyond names: %#v != %#v", *normalized, got)
			}
		})
	}
}

func TestSlackManifestRejectsUnknownEnvironment(t *testing.T) {
	for _, arguments := range [][]string{nil, {"staging"}, {"dev", "ignored"}} {
		var stdout bytes.Buffer
		command := exec.Command("./scripts/render-slack-manifest", arguments...)
		command.Stdout = &stdout
		if err := command.Run(); err == nil || stdout.Len() != 0 {
			t.Fatalf("arguments = %v, error = %v, stdout = %q", arguments, err, stdout.String())
		}
	}
}
