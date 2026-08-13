package metrics

import (
	"bufio"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type HostMetrics struct {
	CPUPercent  float64        `json:"cpu_percent"`
	CPUCores    int            `json:"cpu_cores"`
	MemTotal    uint64         `json:"mem_total"`
	MemUsed     uint64         `json:"mem_used"`
	MemPercent  float64        `json:"mem_percent"`
	DiskTotal   uint64         `json:"disk_total"`
	DiskUsed    uint64         `json:"disk_used"`
	DiskFree    uint64         `json:"disk_free"`
	DiskPercent float64        `json:"disk_percent"`
	NetRx       uint64         `json:"net_rx"`
	NetTx       uint64         `json:"net_tx"`
	Load1       float64        `json:"load1"`
	GPU         []GPUInfo      `json:"gpu"`
	Timestamp   int64          `json:"timestamp"`
	Extra       map[string]any `json:"extra,omitempty"`
}

type GPUInfo struct {
	Name        string  `json:"name"`
	UtilPercent float64 `json:"util_percent"`
	MemUsedMB   float64 `json:"mem_used_mb"`
	MemTotalMB  float64 `json:"mem_total_mb"`
}

type Hub struct {
	mu        sync.Mutex
	clients   map[*websocket.Conn]struct{}
	agentSock string
	upgrader  websocket.Upgrader
}

func NewHub(agentSock string) *Hub {
	return &Hub{
		clients:   map[*websocket.Conn]struct{}{},
		agentSock: agentSock,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.mu.Lock()
	h.clients[conn] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, conn)
		h.mu.Unlock()
		_ = conn.Close()
	}()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (h *Hub) Start() {
	go func() {
		t := time.NewTicker(1 * time.Second)
		defer t.Stop()
		for range t.C {
			m := h.collect()
			b, _ := json.Marshal(m)
			h.mu.Lock()
			for c := range h.clients {
				if err := c.WriteMessage(websocket.TextMessage, b); err != nil {
					_ = c.Close()
					delete(h.clients, c)
				}
			}
			h.mu.Unlock()
		}
	}()
}

func (h *Hub) Snapshot() HostMetrics {
	m := h.collect()
	if m.CPUCores <= 0 {
		m.CPUCores = runtime.NumCPU()
	}
	if m.DiskFree == 0 && m.DiskTotal > m.DiskUsed {
		m.DiskFree = m.DiskTotal - m.DiskUsed
	}
	return m
}

func (h *Hub) collect() HostMetrics {
	if m, ok := h.fromAgent(); ok {
		return m
	}
	return collectLocal()
}

func (h *Hub) fromAgent() (HostMetrics, bool) {
	if h.agentSock == "" {
		return HostMetrics{}, false
	}
	conn, err := net.DialTimeout("unix", h.agentSock, 200*time.Millisecond)
	if err != nil {
		return HostMetrics{}, false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(500 * time.Millisecond))
	_, _ = conn.Write([]byte("GET\n"))
	dec := json.NewDecoder(conn)
	var m HostMetrics
	if err := dec.Decode(&m); err != nil {
		return HostMetrics{}, false
	}
	return m, true
}

func collectLocal() HostMetrics {
	m := HostMetrics{Timestamp: time.Now().Unix(), GPU: []GPUInfo{}}
	m.CPUPercent = readCPU()
	m.CPUCores = runtime.NumCPU()
	m.MemTotal, m.MemUsed, m.MemPercent = readMem()
	m.DiskTotal, m.DiskUsed, m.DiskFree, m.DiskPercent = readDisk("/")
	m.NetRx, m.NetTx = readNet()
	m.Load1 = readLoad()
	if m.DiskFree == 0 && m.DiskTotal > m.DiskUsed {
		m.DiskFree = m.DiskTotal - m.DiskUsed
	}
	return m
}

var (
	prevCPUUser, prevCPUNice, prevCPUSystem, prevCPUIdle, prevCPUIO, prevCPUIRQ, prevCPUSoft, prevCPUSteal uint64
	prevCPUOnce                                                                                            bool
)

func readCPU() float64 {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 8 || fields[0] != "cpu" {
		return 0
	}
	vals := make([]uint64, 8)
	for i := 0; i < 8; i++ {
		vals[i], _ = strconv.ParseUint(fields[i+1], 10, 64)
	}
	user, nice, system, idle, iowait, irq, soft, steal := vals[0], vals[1], vals[2], vals[3], vals[4], vals[5], vals[6], vals[7]
	if !prevCPUOnce {
		prevCPUUser, prevCPUNice, prevCPUSystem, prevCPUIdle = user, nice, system, idle
		prevCPUIO, prevCPUIRQ, prevCPUSoft, prevCPUSteal = iowait, irq, soft, steal
		prevCPUOnce = true
		return 0
	}
	idleAll := idle + iowait
	prevIdleAll := prevCPUIdle + prevCPUIO
	nonIdle := user + nice + system + irq + soft + steal
	prevNonIdle := prevCPUUser + prevCPUNice + prevCPUSystem + prevCPUIRQ + prevCPUSoft + prevCPUSteal
	total := idleAll + nonIdle
	prevTotal := prevIdleAll + prevNonIdle
	td := float64(total - prevTotal)
	id := float64(idleAll - prevIdleAll)
	prevCPUUser, prevCPUNice, prevCPUSystem, prevCPUIdle = user, nice, system, idle
	prevCPUIO, prevCPUIRQ, prevCPUSoft, prevCPUSteal = iowait, irq, soft, steal
	if td <= 0 {
		return 0
	}
	return (td - id) / td * 100
}

func readMem() (total, used uint64, pct float64) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return
	}
	var free, buffers, cached uint64
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		v *= 1024
		switch fields[0] {
		case "MemTotal:":
			total = v
		case "MemAvailable:":
			free = v
		case "Buffers:":
			buffers = v
		case "Cached:":
			cached = v
		}
	}
	if free == 0 {
		free = buffers + cached
	}
	if total > free {
		used = total - free
	}
	if total > 0 {
		pct = float64(used) / float64(total) * 100
	}
	return
}

func readDisk(path string) (total, used, free uint64, pct float64) {
	return diskUsage(path)
}

func readNet() (rx, tx uint64) {
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		name := strings.TrimSpace(parts[0])
		if name == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(fields[0], 10, 64)
		t, _ := strconv.ParseUint(fields[8], 10, 64)
		rx += r
		tx += t
	}
	return
}

func readLoad() float64 {
	b, err := os.ReadFile("/proc/loadavg")
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
