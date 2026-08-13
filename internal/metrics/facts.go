package metrics

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Facts struct {
	Hostname  string  `json:"hostname"`
	OS        string  `json:"os"`
	Kernel    string  `json:"kernel"`
	Arch      string  `json:"arch"`
	CPUModel  string  `json:"cpu_model"`
	CPUCores  int     `json:"cpu_cores"`
	SSHPort   int     `json:"ssh_port"`
	PublicIP  string  `json:"public_ip"`
	PrimaryIP string  `json:"primary_ip"`
	Virt      string  `json:"virt"`
	UptimeSec float64 `json:"uptime_sec"`
}

func CollectFacts() Facts {
	hn, _ := os.Hostname()
	f := Facts{
		Hostname:  hn,
		OS:        readOSRelease(),
		Kernel:    strings.TrimSpace(readFile("/proc/sys/kernel/osrelease")),
		Arch:      runtime.GOARCH,
		CPUModel:  readCPUModel(),
		CPUCores:  runtime.NumCPU(),
		SSHPort:   readSSHPort(),
		PublicIP:  outboundIP(),
		PrimaryIP: primaryIPv4(),
		Virt:      readVirt(),
		UptimeSec: readUptime(),
	}
	if f.Kernel == "" {
		f.Kernel = runtime.GOOS
	}
	if f.OS == "" {
		f.OS = runtime.GOOS
	}
	if f.SSHPort <= 0 {
		f.SSHPort = 22
	}
	return f
}

func FormatUptime(sec float64) string {
	if sec <= 0 {
		return "—"
	}
	d := time.Duration(sec) * time.Second
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	mins := int(d.Minutes()) % 60
	if days > 0 {
		return strconv.Itoa(days) + "d " + strconv.Itoa(hours) + "h"
	}
	if hours > 0 {
		return strconv.Itoa(hours) + "h " + strconv.Itoa(mins) + "m"
	}
	return strconv.Itoa(mins) + "m"
}

func readOSRelease() string {
	b, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return ""
}

func readCPUModel() string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var model, hardware string
	for sc.Scan() {
		line := sc.Text()
		if k, v, ok := strings.Cut(line, ":"); ok {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			switch k {
			case "model name", "Model Name":
				if model == "" {
					model = v
				}
			case "Hardware":
				if hardware == "" {
					hardware = v
				}
			}
		}
	}
	if model != "" {
		return model
	}
	return hardware
}

func readSSHPort() int {
	port := 0
	files := []string{"/etc/ssh/sshd_config"}
	if matches, _ := filepath.Glob("/etc/ssh/sshd_config.d/*.conf"); len(matches) > 0 {
		files = append(files, matches...)
	}
	for _, path := range files {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 && strings.EqualFold(fields[0], "Port") {
				if n, err := strconv.Atoi(fields[1]); err == nil && n > 0 && n < 65536 {
					port = n
				}
			}
		}
	}
	if port > 0 {
		return port
	}
	return 22
}

func readUptime() float64 {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	v, _ := strconv.ParseFloat(fields[0], 64)
	return v
}

func primaryIPv4() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		if skipIface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP.To4()
			if ip == nil || ip.IsLoopback() {
				continue
			}
			return ip.String()
		}
	}
	return ""
}

func skipIface(name string) bool {
	n := strings.ToLower(name)
	if n == "lo" || n == "docker0" {
		return true
	}
	prefixes := []string{"br-", "veth", "virbr", "cni", "flannel", "calico", "weave", "tun", "tap", "dummy", "sit", "docker"}
	for _, p := range prefixes {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

func readVirt() string {
	vendor := strings.TrimSpace(readFile("/sys/class/dmi/id/sys_vendor"))
	product := strings.TrimSpace(readFile("/sys/class/dmi/id/product_name"))
	combo := strings.ToLower(vendor + " " + product)
	switch {
	case strings.Contains(combo, "qemu") || strings.Contains(combo, "kvm"):
		return "KVM"
	case strings.Contains(combo, "xen"):
		return "Xen"
	case strings.Contains(combo, "vmware"):
		return "VMware"
	case strings.Contains(combo, "virtualbox"):
		return "VirtualBox"
	case strings.Contains(combo, "microsoft") || strings.Contains(combo, "hyper-v"):
		return "Hyper-V"
	case strings.Contains(combo, "google"):
		return "Google"
	case strings.Contains(combo, "amazon") || strings.Contains(combo, "ec2"):
		return "AWS"
	case strings.Contains(combo, "digitalocean"):
		return "DigitalOcean"
	}
	if vendor != "" && !strings.EqualFold(vendor, "none") {
		if product != "" && !strings.EqualFold(product, "none") {
			return strings.TrimSpace(vendor + " " + product)
		}
		return vendor
	}
	return "—"
}

func outboundIP() string {
	conn, err := net.DialTimeout("udp", "8.8.8.8:80", 800*time.Millisecond)
	if err != nil {
		return ""
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil {
		return ""
	}
	ip := addr.IP.To4()
	if ip == nil {
		return addr.IP.String()
	}
	return ip.String()
}

func readFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
