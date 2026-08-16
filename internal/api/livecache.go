package api

import (
	"time"

	"github.com/x5coder/vps-rooms/internal/rooms"
)

func (s *Server) startLiveCache() {
	s.liveUsage = map[string]int64{}
	s.liveDisk = map[string]rooms.DiskStats{}
	s.liveStatus = map[string]string{}
	go func() {
		s.refreshLiveCache(true)
		fast := time.NewTicker(2 * time.Second)
		slow := time.NewTicker(20 * time.Second)
		defer fast.Stop()
		defer slow.Stop()
		for {
			select {
			case <-fast.C:
				if s.anyJob() {
					continue
				}
				s.refreshLiveCache(false)
			case <-slow.C:
				if s.anyJob() {
					continue
				}
				s.refreshLiveCache(true)
			}
		}
	}()
}

func (s *Server) refreshLiveCache(withUsage bool) {
	if !s.liveRefreshing.CompareAndSwap(false, true) {
		return
	}
	defer s.liveRefreshing.Store(false)

	if s.Docker != nil && s.Docker.Available() {
		if ports, err := s.Docker.UsedPorts(); err == nil {
			s.liveMu.Lock()
			s.livePorts = ports
			s.liveMu.Unlock()
		}
	}
	roomsList, err := s.Store.ListRooms()
	if err != nil {
		return
	}
	status := map[string]string{}
	usage := map[string]int64{}
	disk := map[string]rooms.DiskStats{}
	for i := range roomsList {
		room := roomsList[i]
		if withUsage {
			d := s.Rooms.DiskStats(room.ID)
			usage[room.ID] = d.Usage
			disk[room.ID] = d
		}
		projs, _ := s.Store.ListProjects(room.ID)
		for _, p := range projs {
			st := p.Status
			if s.Docker != nil && p.ContainerID != "" {
				if st2, e := s.Docker.InspectStatus(p.ContainerID); e == nil {
					st = st2
				}
			}
			status[p.ID] = st
		}
	}
	s.liveMu.Lock()
	s.liveStatus = status
	if withUsage && len(usage) > 0 {
		s.liveUsage = usage
		s.liveDisk = disk
	}
	s.liveMu.Unlock()
}

func (s *Server) cachedPorts() []int {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	out := make([]int, len(s.livePorts))
	copy(out, s.livePorts)
	return out
}

func (s *Server) cachedUsage(roomID string) int64 {
	return s.cachedDisk(roomID).Usage
}

func (s *Server) cachedDisk(roomID string) rooms.DiskStats {
	s.liveMu.Lock()
	d, ok := s.liveDisk[roomID]
	s.liveMu.Unlock()
	if ok {
		return d
	}
	if s.Rooms == nil {
		return rooms.DiskStats{}
	}
	return s.Rooms.DiskStats(roomID)
}

func (s *Server) cachedStatus(projectID string) string {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if s.liveStatus == nil {
		return ""
	}
	return s.liveStatus[projectID]
}
