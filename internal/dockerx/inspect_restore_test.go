package dockerx

import "testing"

func TestParseContainerInspectArray(t *testing.T) {
	raw := []byte(`[{"Name":"/web","Config":{"Image":"nginx:latest","Env":["FOO=1"],"Cmd":["nginx"]},
		"HostConfig":{"Binds":["data:/var/lib"],"RestartPolicy":{"Name":"unless-stopped"},
		"PortBindings":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}]}}}]`)
	spec, err := ParseContainerInspect(raw)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Config.Image != "nginx:latest" {
		t.Fatalf("image %s", spec.Config.Image)
	}
	if spec.Name != "/web" {
		t.Fatalf("name %s", spec.Name)
	}
	if len(spec.HostConfig.Binds) != 1 {
		t.Fatal("binds")
	}
	if spec.HostConfig.PortBindings["80/tcp"][0].HostPort != "8080" {
		t.Fatal("port")
	}
}
