package dockerx

import "testing"

func TestRegistryPullable(t *testing.T) {
	if RegistryPullable("vpsrooms/admin:latest") {
		t.Fatal("local vpsrooms image")
	}
	if !RegistryPullable("docker.n8n.io/n8nio/n8n:latest") {
		t.Fatal("n8n")
	}
	if !RegistryPullable("kong/kong:3.9.1") {
		t.Fatal("kong")
	}
	if !RegistryPullable("supabase/postgres:15.8.1.085") {
		t.Fatal("supabase")
	}
}

func TestLocalOwnedImage(t *testing.T) {
	if !LocalOwnedImage("vpsrooms/linkedin-auto:latest") || !LocalOwnedImage("vps-ci:abc") {
		t.Fatal("should own local tags")
	}
	if LocalOwnedImage("kong/kong:3.9.1") || LocalOwnedImage("docker.n8n.io/n8nio/n8n:latest") {
		t.Fatal("must not delete public images")
	}
}
