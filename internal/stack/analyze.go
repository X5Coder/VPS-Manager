package stack

import (
	"os"
	"regexp"
	"strings"

	"github.com/x5coder/vps-rooms/internal/dockerx"
	"github.com/x5coder/vps-rooms/internal/store"
)

type ComposeInfo struct {
	Path     string   `json:"path"`
	Services []string `json:"services"`
	Images   []string `json:"images"`
	Volumes  []string `json:"volumes"`
	Networks []string `json:"networks"`
	OK       bool     `json:"ok"`
	Error    string   `json:"error,omitempty"`
}

var (
	reServiceKey = regexp.MustCompile(`(?m)^  ([A-Za-z0-9._-]+):`)
	reImage      = regexp.MustCompile(`(?m)^\s+image:\s*(\S+)`)
	reVolName    = regexp.MustCompile(`(?m)^  ([A-Za-z0-9._-]+):`)
)

func ComposeProject(roomID string) string {
	return "vr" + store.ShortRoomID(roomID)
}

func AnalyzeComposeDir(dir string) ComposeInfo {
	info := ComposeInfo{Services: []string{}, Images: []string{}, Volumes: []string{}, Networks: []string{}}
	file := dockerx.ComposeFile(dir)
	if file == "" {
		info.Error = "compose.yml not found"
		return info
	}
	info.Path = file
	b, err := os.ReadFile(file)
	if err != nil {
		info.Error = err.Error()
		return info
	}
	text := string(b)
	info.Services = sectionKeys(text, "services:")
	info.Volumes = sectionKeys(text, "volumes:")
	info.Networks = sectionKeys(text, "networks:")
	seen := map[string]bool{}
	for _, m := range reImage.FindAllStringSubmatch(text, -1) {
		img := strings.Trim(m[1], `"'`)
		if img == "" || seen[img] {
			continue
		}
		seen[img] = true
		info.Images = append(info.Images, img)
	}
	if len(info.Services) == 0 {
		info.Error = "no services in compose.yml"
		return info
	}
	info.OK = true
	return info
}

func sectionKeys(text, header string) []string {
	i := strings.Index(text, header)
	if i < 0 {
		return nil
	}
	rest := text[i+len(header):]
	next := -1
	for _, h := range []string{"\nservices:", "\nvolumes:", "\nnetworks:", "\nconfigs:", "\nsecrets:"} {
		if h == "\n"+header {
			continue
		}
		if j := strings.Index(rest, h); j >= 0 && (next < 0 || j < next) {
			next = j
		}
	}
	block := rest
	if next >= 0 {
		block = rest[:next]
	}
	var out []string
	for _, m := range reServiceKey.FindAllStringSubmatch(block, -1) {
		out = append(out, m[1])
	}
	return out
}
