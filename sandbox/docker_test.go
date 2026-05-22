package sandbox

import "testing"

func TestVolumeNameSanitizesServerID(t *testing.T) {
	got := volumeName("vespra test", "DM:user/../../bad")
	if got != "vespra-test-DM-user-..-..-bad" {
		t.Fatalf("volumeName() = %q", got)
	}
}

func TestDockerExecutorRejectsDMServerID(t *testing.T) {
	exec := &DockerExecutor{DockerPath: "docker"}
	_, err := exec.Run(t.Context(), RunRequest{
		ServerID:        "DM:user1",
		Command:         "echo hi",
		JobImage:        "image",
		VolumePrefix:    "prefix",
		EgressNetwork:   "egress",
		MaxCommandBytes: 100,
	})
	if err == nil {
		t.Fatal("Run() should reject DM server IDs")
	}
}

func TestDockerExecutorRejectsHostNetwork(t *testing.T) {
	exec := &DockerExecutor{DockerPath: "docker"}
	_, err := exec.Run(t.Context(), RunRequest{
		ServerID:        "srv1",
		Command:         "echo hi",
		JobImage:        "image",
		VolumePrefix:    "prefix",
		EgressNetwork:   "host",
		MaxCommandBytes: 100,
	})
	if err == nil {
		t.Fatal("Run() should reject host network")
	}
}
